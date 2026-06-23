package page

import "testing"

func TestClamp(t *testing.T) {
	cases := []struct {
		name                  string
		inLimit, inOffset     int
		wantLimit, wantOffset int
	}{
		{"zero limit -> default", 0, 0, DefaultLimit, 0},
		{"negative limit -> default", -5, 0, DefaultLimit, 0},
		{"over cap -> default", MaxLimit + 1, 0, DefaultLimit, 0},
		{"way over cap -> default", 100000, 0, DefaultLimit, 0},
		{"at cap kept", MaxLimit, 0, MaxLimit, 0},
		{"in-range kept", 10, 0, 10, 0},
		{"one -> one", 1, 0, 1, 0},
		{"negative offset -> zero", 20, -3, 20, 0},
		{"positive offset kept", 20, 40, 20, 40},
		{"out-of-range limit with valid offset", 0, 25, DefaultLimit, 25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLimit, gotOffset := Clamp(c.inLimit, c.inOffset)
			if gotLimit != c.wantLimit || gotOffset != c.wantOffset {
				t.Fatalf("Clamp(%d, %d) = (%d, %d), want (%d, %d)",
					c.inLimit, c.inOffset, gotLimit, gotOffset, c.wantLimit, c.wantOffset)
			}
		})
	}
}

// TestClampNeverExceedsMax guards the invariant the whole change depends on: no input
// can produce a page larger than MaxLimit.
func TestClampNeverExceedsMax(t *testing.T) {
	for _, in := range []int{-1, 0, 1, 49, 50, 99, 100, 101, 1 << 20} {
		if got, _ := Clamp(in, 0); got > MaxLimit || got <= 0 {
			t.Fatalf("Clamp(%d, 0) limit = %d, want in (0, %d]", in, got, MaxLimit)
		}
	}
}
