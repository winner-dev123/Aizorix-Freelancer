// Package rbac provides authorization helpers used by every service's transport layer.
// RBAC (role -> permission) is combined with lightweight ABAC guards (e.g. "is a party
// to this contract") because ownership checks cannot be expressed by roles alone.
package rbac

import "errors"

var ErrForbidden = errors.New("rbac: forbidden")

// Principal is derived from the verified JWT claims by gateway/interceptor middleware.
type Principal struct {
	UserID      string
	Roles       []string
	Permissions []string
	AccountType string
}

func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func (p Principal) Can(permission string) bool {
	for _, perm := range p.Permissions {
		if perm == permission || perm == "*" {
			return true
		}
	}
	return false
}

// Require returns ErrForbidden unless the principal holds the permission.
func (p Principal) Require(permission string) error {
	if p.Can(permission) {
		return nil
	}
	return ErrForbidden
}

// ABAC guard: the actor must be one of the named user ids (e.g. contract parties).
func RequireOneOf(actor string, allowed ...string) error {
	for _, a := range allowed {
		if a == actor {
			return nil
		}
	}
	return ErrForbidden
}
