package activity

import (
	"testing"
	"time"
)

func TestIntensityDiminishingReturns(t *testing.T) {
	p := DefaultParams()
	low := p.Intensity(Sample{KeyboardCount: 5, MouseCount: 3, MouseDistance: 200})
	high := p.Intensity(Sample{KeyboardCount: 120, MouseCount: 90, MouseDistance: 8000})
	if !(low > 0 && low < high) {
		t.Fatalf("expected 0<low<high, got low=%.3f high=%.3f", low, high)
	}
	if high > 1.0 {
		t.Fatalf("intensity must be clamped to 1, got %.3f", high)
	}
	// A single keystroke per minute should NOT read as fully active.
	single := p.Intensity(Sample{KeyboardCount: 1})
	if single >= 0.5 {
		t.Fatalf("single keystroke should be low intensity, got %.3f", single)
	}
}

func TestComputeBusySlice(t *testing.T) {
	p := DefaultParams()
	start := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	var samples []Sample
	for i := 0; i < 10; i++ { // a sample each minute, genuinely busy with variance
		samples = append(samples, Sample{
			At:            start.Add(time.Duration(i) * time.Minute),
			KeyboardCount: 40 + i*3,
			MouseCount:    20 + (i%4)*5,
			MouseDistance: 1500 + i*200,
		})
	}
	r := p.Compute(samples, start, end)
	if r.ActivityPct < 50 {
		t.Fatalf("busy slice should be reasonably active, got %d%%", r.ActivityPct)
	}
}

func TestComputeDetectsMacro(t *testing.T) {
	p := DefaultParams()
	start := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	var samples []Sample
	for i := 0; i < 10; i++ { // identical input every minute => macro signature
		samples = append(samples, Sample{
			At:            start.Add(time.Duration(i) * time.Minute),
			KeyboardCount: 50, MouseCount: 30, MouseDistance: 2000,
		})
	}
	r := p.Compute(samples, start, end)
	if !r.Suspicious {
		t.Fatalf("expected uniform input to be flagged suspicious, reasons=%v", r.SuspectReasons)
	}
}

func TestComputeIdle(t *testing.T) {
	p := DefaultParams()
	start := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	// Only first two minutes have input; rest is idle.
	samples := []Sample{
		{At: start, KeyboardCount: 60, MouseCount: 40, MouseDistance: 3000},
		{At: start.Add(time.Minute), KeyboardCount: 55, MouseCount: 35, MouseDistance: 2500},
	}
	r := p.Compute(samples, start, end)
	if r.IdleSeconds == 0 {
		t.Fatal("expected idle time to be detected")
	}
	if r.ActivityPct >= 100 {
		t.Fatalf("idle slice should not be 100%%, got %d", r.ActivityPct)
	}
}
