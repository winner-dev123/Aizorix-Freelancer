// Package token issues and verifies ES256 access tokens and provides JWKS so that
// every service verifies tokens locally (no per-request call to auth on the hot path).
package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the access-token body. Keep it small — it is sent on every request.
type Claims struct {
	jwt.RegisteredClaims
	UserID            string   `json:"uid"`
	SessionID         string   `json:"sid"`
	Roles             []string `json:"roles"`
	Permissions       []string `json:"perms,omitempty"`
	ResidencyCountry  string   `json:"rc,omitempty"`
	AccountType       string   `json:"act,omitempty"`
}

// Issuer signs access tokens with the current private key. `kid` is embedded so verifiers
// pick the matching public key from JWKS during rotation (overlapping keys supported).
type Issuer struct {
	priv      *ecdsa.PrivateKey
	kid       string
	issuer    string
	audience  string
	accessTTL time.Duration
}

func NewIssuer(priv *ecdsa.PrivateKey, kid, issuer, audience string, accessTTL time.Duration) *Issuer {
	return &Issuer{priv: priv, kid: kid, issuer: issuer, audience: audience, accessTTL: accessTTL}
}

func (i *Issuer) Issue(c Claims) (string, error) {
	now := time.Now()
	c.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    i.issuer,
		Audience:  jwt.ClaimStrings{i.audience},
		Subject:   c.UserID,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(i.accessTTL)),
		ID:        newJTI(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = i.kid
	return tok.SignedString(i.priv)
}

// Verifier validates tokens against a set of public keys keyed by kid (a JWKS).
type Verifier struct {
	keys     map[string]*ecdsa.PublicKey
	issuer   string
	audience string
}

func NewVerifier(keys map[string]*ecdsa.PublicKey, issuer, audience string) *Verifier {
	return &Verifier{keys: keys, issuer: issuer, audience: audience}
}

var ErrInvalidToken = errors.New("token: invalid")

func (v *Verifier) Verify(raw string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("token: unexpected alg %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := v.keys[kid]
		if !ok {
			return nil, fmt.Errorf("token: unknown kid %q", kid)
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	return claims, nil
}

// JWKS renders the public keys as a JSON Web Key Set served at /.well-known/jwks.json.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func MarshalJWKS(keys map[string]*ecdsa.PublicKey) ([]jwk, error) {
	out := make([]jwk, 0, len(keys))
	for kid, pub := range keys {
		if pub.Curve != elliptic.P256() {
			return nil, errors.New("token: only P-256 supported")
		}
		out = append(out, jwk{
			Kty: "EC", Crv: "P-256", Kid: kid, Use: "sig", Alg: "ES256",
			X: b64(pub.X), Y: b64(pub.Y),
		})
	}
	return out, nil
}

// ParseJWKS parses a JWKS document (as served at /.well-known/jwks.json) into a kid->key map
// suitable for NewVerifier. The API gateway and every service fetch this on startup (and
// refresh periodically) so they verify ES256 tokens locally without calling auth per request.
func ParseJWKS(jwksJSON []byte) (map[string]*ecdsa.PublicKey, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(jwksJSON, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]*ecdsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" {
			continue
		}
		xb, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("token: bad jwk x: %w", err)
		}
		yb, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("token: bad jwk y: %w", err)
		}
		out[k.Kid] = &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xb),
			Y:     new(big.Int).SetBytes(yb),
		}
	}
	if len(out) == 0 {
		return nil, errors.New("token: no usable EC keys in JWKS")
	}
	return out, nil
}

func b64(n *big.Int) string {
	// EC coordinates are fixed 32 bytes for P-256, left-padded.
	b := n.Bytes()
	if len(b) < 32 {
		pad := make([]byte, 32-len(b))
		b = append(pad, b...)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// newJTI returns a unique, unpredictable token id from the CSPRNG (not a hashed
// timestamp), so jti is safe to rely on for replay defense / deny-listing.
func newJTI() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read never returns an error on supported platforms; fall back defensively.
		now := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(now >> (8 * i))
		}
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
