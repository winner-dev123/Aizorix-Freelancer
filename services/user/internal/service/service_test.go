package service

import (
	"context"
	"errors"
	"testing"
)

// TestValidationGuards pins the input validation that runs before any store access (a nil store is
// therefore fine): profile upserts, skills, portfolio, and device listing all require their key
// identifiers, and the freelancer experience level must be a known enum value.
func TestValidationGuards(t *testing.T) {
	svc := New(nil)
	ctx := context.Background()

	if _, err := svc.UpsertFreelancerProfile(ctx, FreelancerProfileInput{UserID: ""}); !errors.Is(err, ErrValidation) {
		t.Errorf("freelancer upsert without user id: %v, want ErrValidation", err)
	}
	if _, err := svc.UpsertClientProfile(ctx, ClientProfileInput{UserID: ""}); !errors.Is(err, ErrValidation) {
		t.Errorf("client upsert without user id: %v, want ErrValidation", err)
	}
	if _, err := svc.UpsertFreelancerProfile(ctx, FreelancerProfileInput{UserID: "u1", Experience: "wizard"}); !errors.Is(err, ErrValidation) {
		t.Errorf("invalid experience enum: %v, want ErrValidation", err)
	}
	if err := svc.AddFreelancerSkill(ctx, "", "s1", "expert", nil); !errors.Is(err, ErrValidation) {
		t.Errorf("skill without user id: %v, want ErrValidation", err)
	}
	if err := svc.AddFreelancerSkill(ctx, "u1", "s1", "wizard", nil); !errors.Is(err, ErrValidation) {
		t.Errorf("skill with invalid level: %v, want ErrValidation", err)
	}
	if _, err := svc.AddPortfolioItem(ctx, "u1", "", "", "", nil, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("portfolio item without title: %v, want ErrValidation", err)
	}
	if _, err := svc.ListDevices(ctx, ""); !errors.Is(err, ErrValidation) {
		t.Errorf("list devices without user id: %v, want ErrValidation", err)
	}
}

// TestRegisterDevice_RequiresWellFormedEd25519Key pins the fail-closed enrollment guard: a device
// is never enrolled with a malformed attestation key (the screenshot service would later reject it
// at confirm time). Only the failure paths are exercised — they return before any store access.
func TestRegisterDevice_RequiresWellFormedEd25519Key(t *testing.T) {
	svc := New(nil)
	ctx := context.Background()
	good := make([]byte, ed25519PublicKeySize)

	if _, err := svc.RegisterDevice(ctx, "", "fp", "Name", good); !errors.Is(err, ErrValidation) {
		t.Errorf("missing user id: %v, want ErrValidation", err)
	}
	if _, err := svc.RegisterDevice(ctx, "u1", "", "Name", good); !errors.Is(err, ErrValidation) {
		t.Errorf("missing fingerprint: %v, want ErrValidation", err)
	}
	// Any length other than 32 bytes must be rejected (fail closed).
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := svc.RegisterDevice(ctx, "u1", "fp", "Name", make([]byte, n)); !errors.Is(err, ErrValidation) {
			t.Errorf("attestation key length %d: err = %v, want ErrValidation", n, err)
		}
	}
}

// TestSetKYCStatus_ValidatesEnum pins that only the kyc_status enum values are accepted (and that
// the check is case-sensitive), both runs returning before any store access.
func TestSetKYCStatus_ValidatesEnum(t *testing.T) {
	svc := New(nil)
	ctx := context.Background()

	if err := svc.SetKYCStatus(ctx, "", "verified", "", ""); !errors.Is(err, ErrValidation) {
		t.Errorf("missing user id: %v, want ErrValidation", err)
	}
	for _, bad := range []string{"", "approved", "VERIFIED", "done"} {
		if err := svc.SetKYCStatus(ctx, "u1", bad, "", ""); !errors.Is(err, ErrKYCNotAllowed) {
			t.Errorf("status %q: err = %v, want ErrKYCNotAllowed", bad, err)
		}
	}
}
