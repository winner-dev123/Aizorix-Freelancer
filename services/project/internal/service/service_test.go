package service

import (
	"context"
	"errors"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// TestValidateBudget pins the per-type budget rules (a pure function): a fixed project needs a
// positive min and a max that is not below the min; an hourly project additionally needs a
// positive weekly hour limit; any other budget type is invalid.
func TestValidateBudget(t *testing.T) {
	cases := []struct {
		name string
		in   CreateProjectInput
		ok   bool
	}{
		{"fixed: min only", CreateProjectInput{BudgetType: "fixed", BudgetMinCents: ptr(int64(1000))}, true},
		{"fixed: min+max", CreateProjectInput{BudgetType: "fixed", BudgetMinCents: ptr(int64(1000)), BudgetMaxCents: ptr(int64(2000))}, true},
		{"fixed: nil min", CreateProjectInput{BudgetType: "fixed"}, false},
		{"fixed: zero min", CreateProjectInput{BudgetType: "fixed", BudgetMinCents: ptr(int64(0))}, false},
		{"fixed: max below min", CreateProjectInput{BudgetType: "fixed", BudgetMinCents: ptr(int64(2000)), BudgetMaxCents: ptr(int64(1000))}, false},
		{"hourly: valid", CreateProjectInput{BudgetType: "hourly", BudgetMinCents: ptr(int64(1000)), WeeklyHourLimit: ptr(40)}, true},
		{"hourly: missing hour limit", CreateProjectInput{BudgetType: "hourly", BudgetMinCents: ptr(int64(1000))}, false},
		{"hourly: zero hour limit", CreateProjectInput{BudgetType: "hourly", BudgetMinCents: ptr(int64(1000)), WeeklyHourLimit: ptr(0)}, false},
		{"hourly: max below min", CreateProjectInput{BudgetType: "hourly", BudgetMinCents: ptr(int64(2000)), BudgetMaxCents: ptr(int64(1000)), WeeklyHourLimit: ptr(40)}, false},
		{"unknown budget type", CreateProjectInput{BudgetType: "weekly", BudgetMinCents: ptr(int64(1000))}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateBudget(c.in)
			if c.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidBudget) {
				t.Fatalf("want ErrInvalidBudget, got %v", err)
			}
		})
	}
}

// TestCreateProject_Guards pins the create-time validation that runs before any store access:
// client id and title are required, the budget type and experience enums are checked, and a
// malformed budget surfaces as ErrInvalidBudget.
func TestCreateProject_Guards(t *testing.T) {
	svc := New(nil)
	ctx := context.Background()
	base := CreateProjectInput{ClientID: "c1", Title: "T", BudgetType: "fixed", BudgetMinCents: ptr(int64(1000))}
	mut := func(f func(*CreateProjectInput)) CreateProjectInput { in := base; f(&in); return in }

	if _, err := svc.CreateProject(ctx, mut(func(in *CreateProjectInput) { in.ClientID = "" })); !errors.Is(err, ErrValidation) {
		t.Errorf("empty client id: %v, want ErrValidation", err)
	}
	if _, err := svc.CreateProject(ctx, mut(func(in *CreateProjectInput) { in.Title = "" })); !errors.Is(err, ErrValidation) {
		t.Errorf("empty title: %v, want ErrValidation", err)
	}
	if _, err := svc.CreateProject(ctx, mut(func(in *CreateProjectInput) { in.BudgetType = "weekly" })); !errors.Is(err, ErrValidation) {
		t.Errorf("bad budget type: %v, want ErrValidation", err)
	}
	if _, err := svc.CreateProject(ctx, mut(func(in *CreateProjectInput) { in.ExperienceRequired = ptr("wizard") })); !errors.Is(err, ErrValidation) {
		t.Errorf("bad experience enum: %v, want ErrValidation", err)
	}
	if _, err := svc.CreateProject(ctx, mut(func(in *CreateProjectInput) { in.BudgetMinCents = nil })); !errors.Is(err, ErrInvalidBudget) {
		t.Errorf("nil budget min: %v, want ErrInvalidBudget", err)
	}
}
