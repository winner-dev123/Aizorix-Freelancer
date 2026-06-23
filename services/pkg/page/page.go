// Package page provides shared limit/offset pagination clamping for list endpoints,
// keeping the default page size and hard cap consistent across services. Clamping lives
// in the data layer so a list query is bounded regardless of which caller invokes it.
package page

// Defaults for limit/offset pagination.
const (
	// DefaultLimit is used when no (or an out-of-range) limit is requested.
	DefaultLimit = 50
	// MaxLimit is the hard cap on a single page; a larger request falls back to DefaultLimit.
	MaxLimit = 100
)

// Clamp normalizes a requested limit/offset pair to safe bounds: a non-positive or
// over-cap limit falls back to DefaultLimit, and a negative offset becomes 0. This bounds
// the result set of list endpoints regardless of caller, preventing unbounded scans.
func Clamp(limit, offset int) (int, int) {
	if limit <= 0 || limit > MaxLimit {
		limit = DefaultLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
