package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeTx is a minimal pgx.Tx that records Exec calls and performs no I/O. WriteLegs only
// uses Exec (after its in-memory zero-sum validation), so the rest of the interface is
// stubbed to satisfy the type and panics if a test accidentally exercises an unused path.
type fakeTx struct {
	execCalls int
	execErr   error
}

func (f *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls++
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) { panic("unused") }
func (f *fakeTx) Commit(ctx context.Context) error          { panic("unused") }
func (f *fakeTx) Rollback(ctx context.Context) error        { panic("unused") }
func (f *fakeTx) CopyFrom(ctx context.Context, t pgx.Identifier, c []string, s pgx.CopyFromSource) (int64, error) {
	panic("unused")
}
func (f *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { panic("unused") }
func (f *fakeTx) LargeObjects() pgx.LargeObjects                               { panic("unused") }
func (f *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	panic("unused")
}
func (f *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	panic("unused")
}
func (f *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { panic("unused") }
func (f *fakeTx) Conn() *pgx.Conn                                               { return nil }

func legs(amounts ...int64) []Leg {
	out := make([]Leg, len(amounts))
	for i, a := range amounts {
		out[i] = Leg{Type: "deposit", AccountKind: "test", AmountCents: a, Currency: "USD"}
	}
	return out
}

func TestWriteLegsBalanced(t *testing.T) {
	s := &Store{}
	cases := []struct {
		name    string
		amounts []int64
	}{
		{"two_leg_balanced", []int64{-1000, 1000}},
		{"three_leg_balanced", []int64{-1000, 900, 100}}, // escrow split w/ fee
		{"all_zero", []int64{0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{}
			if err := s.WriteLegs(context.Background(), tx, "grp-1", legs(tc.amounts...)); err != nil {
				t.Fatalf("balanced legs %v rejected: %v", tc.amounts, err)
			}
			if tx.execCalls != len(tc.amounts) {
				t.Fatalf("expected %d inserts, got %d", len(tc.amounts), tx.execCalls)
			}
		})
	}
}

func TestWriteLegsUnbalancedRejected(t *testing.T) {
	s := &Store{}
	cases := []struct {
		name    string
		amounts []int64
	}{
		{"off_by_one", []int64{-1000, 999}},
		{"missing_credit", []int64{-1000}},
		{"double_debit", []int64{-1000, -1000, 1000}},
		{"positive_sum", []int64{1000, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{}
			err := s.WriteLegs(context.Background(), tx, "grp-x", legs(tc.amounts...))
			if err == nil {
				t.Fatalf("unbalanced legs %v were accepted", tc.amounts)
			}
			if !strings.Contains(err.Error(), "unbalanced ledger") {
				t.Fatalf("expected unbalanced-ledger error, got %v", err)
			}
			// Validation must short-circuit BEFORE any DB write.
			if tx.execCalls != 0 {
				t.Fatalf("unbalanced group must not write any legs, got %d execs", tx.execCalls)
			}
		})
	}
}
