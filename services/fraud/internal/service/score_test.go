package service

import (
	"math"
	"testing"
)

// scoreOf mirrors the store's RecentSignalSum clamp: a weighted sum over the
// contributing signals, capped to [0, 1.000]. The store computes this against the
// database; here we exercise the pure scoring/banding/threshold logic the service
// relies on so the model's behavior is pinned without needing Postgres.
func scoreOf(weights ...float64) float64 {
	var s float64
	for _, w := range weights {
		s += w
	}
	if s > 1.0 {
		s = 1.0
	}
	if s < 0 {
		s = 0
	}
	return s
}

func TestWeightedScoreClamp(t *testing.T) {
	cases := []struct {
		name    string
		weights []float64
		want    float64
	}{
		{"empty", nil, 0},
		{"single", []float64{0.4}, 0.4},
		{"sum", []float64{0.2, 0.3, 0.1}, 0.6},
		{"clamp_high", []float64{0.7, 0.5, 0.9}, 1.0},
		{"clamp_low", []float64{-0.5, 0.1}, 0},
		{"exact_one", []float64{0.5, 0.5}, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreOf(tc.weights...)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("scoreOf(%v) = %v, want %v", tc.weights, got, tc.want)
			}
		})
	}
}

func TestBandThresholds(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0.0, "low"},
		{0.29, "low"},
		{0.3, "medium"}, // lower boundary of medium is inclusive
		{0.59, "medium"},
		{0.6, "high"}, // lower boundary of high is inclusive
		{0.84, "high"},
		{0.85, "critical"}, // == caseThreshold => critical
		{1.0, "critical"},
	}
	for _, tc := range cases {
		if got := band(tc.score); got != tc.want {
			t.Fatalf("band(%v) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

// TestBandMatchesCaseThreshold pins the contract between the banding cutoffs and the
// case-opening threshold: a score opens a case iff it lands in the 'critical' band.
func TestBandMatchesCaseThreshold(t *testing.T) {
	if band(caseThreshold) != "critical" {
		t.Fatalf("score at caseThreshold (%v) must be 'critical', got %q", caseThreshold, band(caseThreshold))
	}
	// Just below the threshold must NOT be critical and must NOT open a case.
	justBelow := caseThreshold - 0.0001
	if band(justBelow) == "critical" {
		t.Fatalf("score %v should not be critical", justBelow)
	}
	if opensCase(justBelow) {
		t.Fatalf("score %v must not open a case", justBelow)
	}
}

// opensCase mirrors the IngestSignal gate (`score >= caseThreshold`).
func opensCase(score float64) bool { return score >= caseThreshold }

func TestCaseOpensPastThreshold(t *testing.T) {
	cases := []struct {
		name    string
		weights []float64
		open    bool
		band    string
	}{
		{"low_no_case", []float64{0.1}, false, "low"},
		{"medium_no_case", []float64{0.3, 0.2}, false, "medium"},
		{"high_no_case", []float64{0.4, 0.3}, false, "high"},
		{"at_threshold_opens", []float64{0.5, 0.35}, true, "critical"},
		{"over_threshold_opens", []float64{0.6, 0.5}, true, "critical"}, // clamps to 1.0
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score := scoreOf(tc.weights...)
			if got := opensCase(score); got != tc.open {
				t.Fatalf("opensCase(score=%v) = %v, want %v", score, got, tc.open)
			}
			if got := band(score); got != tc.band {
				t.Fatalf("band(score=%v) = %q, want %q", score, got, tc.band)
			}
		})
	}
}
