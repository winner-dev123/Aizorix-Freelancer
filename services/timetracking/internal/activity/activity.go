// Package activity contains the algorithms that turn raw input samples from the desktop
// tracker into a billable, fraud-resistant activity percentage. This is the heart of the
// "verified hourly work" differentiator (Phase 9).
//
// Definitions
// -----------
//   slice            A fixed billing/screenshot window, default 600s (10 min). The platform
//                    captures one screenshot per slice and bills in slice units.
//   sample           A short measurement window inside a slice, default 60s, over which the
//                    tracker reports keyboard count, mouse count, and mouse travel.
//   active sample    A sample with input above the noise floor AND not classified idle.
//   idle             A continuous span with no input for >= IdleThreshold (default 300s).
//
// Activity percentage
// -------------------
// The naive "active_seconds / slice_seconds" is gameable (a single key every minute reads as
// 100%). We instead score each sample on input *intensity* with diminishing returns, clamp
// it, subtract idle time, and average across the slice:
//
//   intensity(sample) = min(1, w_k * f(kb) + w_m * f(mouse) + w_d * f(dist))
//       where f(x) = log1p(x) / log1p(saturate_x)   // diminishing returns, saturates at "busy"
//   billable(bucket)  = window * clamp((intensity - ActiveFloor)/(FullActiveAt - ActiveFloor), 0, 1)
//       graded, NOT all-or-nothing: trivial input bills a few seconds, sustained input the full window
//   activity_pct      = 100 * (sum of billable seconds) / (slice_seconds - excused_idle)
//
// excused_idle covers legitimately non-input work (reading, calls) only up to a cap so it
// can't be abused; beyond the cap, idle counts against activity. Manual time and screenshots
// with zero activity feed the fraud service rather than being silently billed.
package activity

import (
	"math"
	"time"
)

// Weights and thresholds. Sourced from config in production so they can be tuned per
// contract category (e.g. design work is mouse-heavy, writing is keyboard-heavy).
type Params struct {
	SliceSeconds   int     // default 600
	SampleSeconds  int     // default 60
	IdleThreshold  int     // seconds of no input that count as idle, default 300
	WeightKeyboard float64 // default 0.5
	WeightMouse    float64 // default 0.3
	WeightDistance float64 // default 0.2
	SaturateKB     float64 // kb events per sample considered "fully busy", default 120
	SaturateMouse  float64 // mouse events per sample considered busy, default 90
	SaturateDist   float64 // mouse pixels per sample considered busy, default 8000
	ActiveFloor    float64 // intensity below this is "present but not working", default 0.08
	FullActiveAt   float64 // intensity at/above which a bucket bills its FULL window, default 0.5
	ExcusedIdleCap float64 // fraction of slice idle that doesn't reduce activity, default 0.25
}

func DefaultParams() Params {
	return Params{
		SliceSeconds: 600, SampleSeconds: 60, IdleThreshold: 300,
		WeightKeyboard: 0.5, WeightMouse: 0.3, WeightDistance: 0.2,
		SaturateKB: 120, SaturateMouse: 90, SaturateDist: 8000,
		ActiveFloor: 0.08, FullActiveAt: 0.5, ExcusedIdleCap: 0.25,
	}
}

// billableFraction grades how much of a sample window is billable by its input intensity:
// 0 below ActiveFloor (present but not working), ramping linearly to 1.0 at FullActiveAt
// (genuinely busy). This is what stops a single keystroke per minute from billing the whole
// minute — trivial input bills a few seconds; only real, sustained input bills the full window.
func (p Params) billableFraction(intensity float64) float64 {
	if intensity < p.ActiveFloor {
		return 0
	}
	span := p.FullActiveAt - p.ActiveFloor
	if span <= 0 {
		return 1
	}
	return math.Min(1, (intensity-p.ActiveFloor)/span)
}

// Sample is one measurement window reported by the tracker.
type Sample struct {
	At            time.Time
	KeyboardCount int
	MouseCount    int
	MouseDistance int // pixels
}

// SliceResult is the computed, billable summary for one slice.
type SliceResult struct {
	ActivityPct   int  // 0..100
	ActiveSeconds int
	IdleSeconds   int
	// Signals forwarded to the fraud service (not used for billing directly).
	Suspicious    bool
	SuspectReasons []string
}

// Intensity scores a single sample in [0,1] using diminishing-returns saturation.
func (p Params) Intensity(s Sample) float64 {
	f := func(x, sat float64) float64 {
		if sat <= 0 {
			return 0
		}
		return math.Min(1, math.Log1p(x)/math.Log1p(sat))
	}
	v := p.WeightKeyboard*f(float64(s.KeyboardCount), p.SaturateKB) +
		p.WeightMouse*f(float64(s.MouseCount), p.SaturateMouse) +
		p.WeightDistance*f(float64(s.MouseDistance), p.SaturateDist)
	return math.Min(1, v)
}

// Compute folds the slice's samples into a billable SliceResult plus fraud signals.
// `samples` should cover the slice window; gaps are treated as no-input (potential idle).
func (p Params) Compute(samples []Sample, sliceStart, sliceEnd time.Time) SliceResult {
	total := int(sliceEnd.Sub(sliceStart).Seconds())
	if total <= 0 {
		return SliceResult{}
	}
	var activeAcc float64 // graded billable seconds (rounded at the end)
	idleSeconds := 0
	var reasons []string

	// Walk the slice in sample-sized steps so missing samples register as idle.
	step := p.SampleSeconds
	byBucket := bucketSamples(samples, sliceStart, step)
	consecutiveIdle := 0
	intensities := make([]float64, 0, total/step+1)

	for offset := 0; offset < total; offset += step {
		seg := minInt(step, total-offset)
		s, ok := byBucket[offset/step]
		intensity := 0.0
		if ok {
			intensity = p.Intensity(s)
		}
		intensities = append(intensities, intensity)

		if frac := p.billableFraction(intensity); frac > 0 {
			// Bill PROPORTIONALLY to intensity, not all-or-nothing: a barely-above-floor
			// bucket bills a few seconds, a genuinely busy one bills the full window.
			activeAcc += float64(seg) * frac
			consecutiveIdle = 0
		} else {
			// Below the floor = no input. Once a continuous gap reaches IdleThreshold, count
			// the WHOLE span as idle (including the seconds before it crossed the threshold),
			// not just the segments after — otherwise the first 300s of every gap is invisible.
			prev := consecutiveIdle
			consecutiveIdle += seg
			if consecutiveIdle >= p.IdleThreshold {
				if prev < p.IdleThreshold {
					idleSeconds += consecutiveIdle // retroactively include the pre-threshold span
				} else {
					idleSeconds += seg
				}
			}
		}
	}
	activeSeconds := int(math.Round(activeAcc))

	// Excused idle: allow up to a cap of idle without penalty (reading/calls).
	excused := int(float64(total) * p.ExcusedIdleCap)
	penalizedIdle := maxInt(0, idleSeconds-excused)
	denom := maxInt(1, total-(idleSeconds-penalizedIdle)) // remove excused idle from denominator
	pct := int(math.Round(100 * float64(activeSeconds) / float64(denom)))
	pct = clamp(pct, 0, 100)

	// ── Fraud signals (do not affect billing here; emitted for the fraud service) ──
	suspicious := false
	// 1) Perfectly uniform intensity across the slice => likely an automation/macro.
	if isTooUniform(intensities) {
		suspicious = true
		reasons = append(reasons, "uniform_input_pattern")
	}
	// 2) Activity at/near 100% with zero idle for a long slice is statistically rare.
	if pct >= 99 && idleSeconds == 0 && total >= 600 {
		suspicious = true
		reasons = append(reasons, "implausible_full_activity")
	}
	// 3) Mouse moves with zero clicks/keys for the whole slice => mouse-jiggler.
	if mouseOnly(samples) {
		suspicious = true
		reasons = append(reasons, "mouse_only_no_keys")
	}

	return SliceResult{
		ActivityPct: pct, ActiveSeconds: activeSeconds, IdleSeconds: idleSeconds,
		Suspicious: suspicious, SuspectReasons: reasons,
	}
}

func bucketSamples(samples []Sample, start time.Time, step int) map[int]Sample {
	m := make(map[int]Sample, len(samples))
	for _, s := range samples {
		idx := int(s.At.Sub(start).Seconds()) / step
		if idx < 0 {
			continue
		}
		// If two samples land in one bucket, keep the busier one.
		if existing, ok := m[idx]; !ok || score(s) > score(existing) {
			m[idx] = s
		}
	}
	return m
}

func score(s Sample) int { return s.KeyboardCount + s.MouseCount + s.MouseDistance/100 }

// isTooUniform flags near-zero variance in non-trivial intensity (macro signature).
func isTooUniform(xs []float64) bool {
	if len(xs) < 5 {
		return false
	}
	var sum float64
	nonzero := 0
	for _, x := range xs {
		sum += x
		if x > 0.01 {
			nonzero++
		}
	}
	if nonzero < len(xs)/2 {
		return false // lots of zeros isn't "uniform busy"
	}
	mean := sum / float64(len(xs))
	if mean < 0.05 {
		return false
	}
	var variance float64
	for _, x := range xs {
		d := x - mean
		variance += d * d
	}
	variance /= float64(len(xs))
	// Coefficient of variation < 1% across many samples is not human.
	return math.Sqrt(variance)/mean < 0.01
}

func mouseOnly(samples []Sample) bool {
	if len(samples) < 5 {
		return false
	}
	keys, mouse := 0, 0
	for _, s := range samples {
		keys += s.KeyboardCount
		mouse += s.MouseCount + s.MouseDistance
	}
	return keys == 0 && mouse > 0
}

func minInt(a, b int) int { if a < b { return a }; return b }
func maxInt(a, b int) int { if a > b { return a }; return b }
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
