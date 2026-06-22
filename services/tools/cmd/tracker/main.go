// Command tracker simulates the desktop tracker's screenshot pipeline against the running
// services, exercising the FULL encrypted path end-to-end:
//
//  1. generate an Ed25519 device key + enroll its public key (user service)
//  2. AES-256-GCM encrypt a screenshot on-device with a local DEK
//  3. request an upload slot (screenshot service KMS-wraps our client DEK)
//  4. PUT the ciphertext directly to S3/MinIO via the presigned URL
//  5. confirm — the server verifies our Ed25519 signature against the ENROLLED key
//  6. authorized view — fetch the wrapped DEK, download, unwrap, decrypt, and verify we
//     recover the exact original bytes.
//
// This proves client-side encryption, the device-key trust anchor, direct-to-S3 upload, and
// decrypt-on-read all work together against real infrastructure.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aizorix/platform/pkg/crypto"
)

func main() {
	var (
		userURL    = env("USER_URL", "http://localhost:8082")
		ssURL      = env("SCREENSHOT_URL", "http://localhost:8086")
		freelancer = env("FREELANCER_ID", "")
		contractID = env("CONTRACT_ID", "")
		masterB64  = env("KMS_LOCAL_MASTER_KEY", "")
	)
	if freelancer == "" || contractID == "" {
		fail("FREELANCER_ID and CONTRACT_ID are required")
	}
	master, err := base64.StdEncoding.DecodeString(masterB64)
	if err != nil || len(master) != 32 {
		fail("KMS_LOCAL_MASTER_KEY must be base64 of 32 bytes (matching the screenshot service)")
	}
	b64 := base64.StdEncoding.EncodeToString

	// ── 1. device key + enrollment ──────────────────────────────────────────
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	enroll := map[string]any{
		"fingerprint":        "tracker-demo-host",
		"display_name":       "Aizorix Tracker (demo)",
		"attestation_pubkey": b64(pub),
	}
	var enrollResp struct {
		DeviceID string `json:"device_id"`
	}
	doJSON(http.MethodPost, userURL+"/v1/users/me/devices", freelancer, enroll, &enrollResp)
	if enrollResp.DeviceID == "" {
		fail("enrollment returned no device_id")
	}
	ok(fmt.Sprintf("1. enrolled device — device_id=%s (Ed25519 pubkey registered)", enrollResp.DeviceID))

	// ── 2. capture + encrypt on-device ──────────────────────────────────────
	plaintext := []byte("PNG\x89...this stands in for a compressed WebP screenshot... " +
		time.Now().Format(time.RFC3339Nano))
	capturedAt := time.Now().UTC().Truncate(time.Second)
	capturedAtStr := capturedAt.Format(time.RFC3339) // canonical: matches the Go server re-derivation
	aad := []byte(contractID + "|" + capturedAtStr)

	clientDEK := make([]byte, 32)
	_, _ = rand.Read(clientDEK)
	block, _ := aes.NewCipher(clientDEK)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	sum := sha256.Sum256(ciphertext)
	// Sign EXACTLY what the screenshot service re-derives: sha256 || captured_at || contract_id.
	sig := ed25519.Sign(priv, append(append(append([]byte{}, sum[:]...), []byte(capturedAtStr)...), []byte(contractID)...))
	ok(fmt.Sprintf("2. encrypted screenshot on-device — %d plaintext -> %d ciphertext bytes (AES-256-GCM)", len(plaintext), len(ciphertext)))

	// ── 3. request upload slot (server wraps our client DEK) ────────────────
	slotReq := map[string]any{
		"contract_id": contractID, "session_id": "00000000-0000-0000-0000-000000000000",
		"slice_id": "", "device_id": enrollResp.DeviceID, "captured_at": capturedAtStr,
		"client_dek": b64(clientDEK),
	}
	var slot struct {
		ScreenshotID string            `json:"screenshot_id"`
		UploadURL    string            `json:"upload_url"`
		S3Key        string            `json:"s3_key"`
		WrappedDEK   string            `json:"wrapped_dek"`
		Headers      map[string]string `json:"required_headers"`
	}
	doJSON(http.MethodPost, ssURL+"/v1/screenshots/upload-slot", freelancer, slotReq, &slot)
	if slot.UploadURL == "" {
		fail("no presigned upload_url returned")
	}
	ok(fmt.Sprintf("3. got upload slot — screenshot_id=%s, key=%s (server KMS-wrapped our DEK)", slot.ScreenshotID, slot.S3Key))

	// ── 4. PUT ciphertext directly to S3/MinIO ──────────────────────────────
	putReq, _ := http.NewRequest(http.MethodPut, slot.UploadURL, bytes.NewReader(ciphertext))
	for k, v := range slot.Headers {
		putReq.Header.Set(k, v)
	}
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil || putResp.StatusCode/100 != 2 {
		code := 0
		if putResp != nil {
			code = putResp.StatusCode
		}
		fail(fmt.Sprintf("S3 PUT failed: err=%v status=%d", err, code))
	}
	putResp.Body.Close()
	ok(fmt.Sprintf("4. uploaded ciphertext to object storage (presigned PUT -> %d)", putResp.StatusCode))

	// ── 5. confirm — server verifies signature vs the ENROLLED device key ───
	confirm := map[string]any{
		"screenshot_id": slot.ScreenshotID, "contract_id": contractID,
		"sha256_cipher": b64(sum[:]), "gcm_nonce": b64(nonce),
		"device_signature": b64(sig), "device_pubkey": b64(pub),
		"captured_at": capturedAtStr, "width": 1920, "height": 1080,
		"size_bytes": len(ciphertext), "format": "webp", "phash": b64([]byte("00000000")),
		"activity_pct": 90,
	}
	var confirmResp struct {
		Accepted bool `json:"accepted"`
	}
	doJSON(http.MethodPost, ssURL+"/v1/screenshots/confirm", freelancer, confirm, &confirmResp)
	if !confirmResp.Accepted {
		fail("confirm rejected")
	}
	ok("5. confirm ACCEPTED — server verified the Ed25519 signature against the enrolled device key")

	// ── 6. authorized view -> download -> unwrap -> decrypt -> verify ───────
	var view struct {
		DownloadURL string `json:"download_url"`
		WrappedDEK  string `json:"wrapped_dek"`
		GCMNonce    string `json:"gcm_nonce"`
		Status      string `json:"status"`
	}
	doJSON(http.MethodGet, ssURL+"/v1/screenshots/"+slot.ScreenshotID, freelancer, nil, &view)
	if view.DownloadURL == "" {
		fail("authorized view returned no download_url")
	}
	dl, err := http.Get(view.DownloadURL)
	if err != nil || dl.StatusCode/100 != 2 {
		fail(fmt.Sprintf("download failed: %v", err))
	}
	downloaded, _ := io.ReadAll(dl.Body)
	dl.Body.Close()

	// Unwrap the DEK via the (local) KMS — the same master key the service used to wrap it.
	kp, _ := crypto.NewLocalKeyProvider(master)
	wrapped, _ := base64.StdEncoding.DecodeString(view.WrappedDEK)
	recoveredDEK, err := kp.UnwrapDEK(wrapped)
	if err != nil {
		fail("KMS unwrap failed: " + err.Error())
	}
	vNonce, _ := base64.StdEncoding.DecodeString(view.GCMNonce)
	vblock, _ := aes.NewCipher(recoveredDEK)
	vgcm, _ := cipher.NewGCM(vblock)
	recovered, err := vgcm.Open(nil, vNonce, downloaded, aad)
	if err != nil {
		fail("decrypt-on-read failed (auth tag mismatch): " + err.Error())
	}
	if !bytes.Equal(recovered, plaintext) {
		fail("decrypted bytes DO NOT match the original")
	}
	ok(fmt.Sprintf("6. authorized view -> downloaded %d bytes -> KMS-unwrapped DEK -> decrypted -> EXACT match (%d bytes)", len(downloaded), len(recovered)))

	fmt.Println("\n==== ENCRYPTED SCREENSHOT PIPELINE VERIFIED END-TO-END ====")
}

// ── helpers ─────────────────────────────────────────────────────────────────

func doJSON(method, url, userID string, body, out any) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", userID) // the gateway injects this from the verified JWT in prod
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail(fmt.Sprintf("%s %s: %v", method, url, err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		fail(fmt.Sprintf("%s %s -> %d: %s", method, url, resp.StatusCode, string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			fail(fmt.Sprintf("decode %s: %v (body=%s)", url, err, string(raw)))
		}
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func ok(m string)   { fmt.Println("  PASS  " + m) }
func fail(m string) { fmt.Fprintln(os.Stderr, "  FAIL  "+m); os.Exit(1) }
