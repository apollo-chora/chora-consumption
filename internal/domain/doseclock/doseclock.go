// Package doseclock owns the platform's "dose day" — the bucket boundary
// every campaign/dose daily mechanic keys on: the D13 qgen budget, the D7
// rung-pacing gate, the deterministic dose election seed, and the
// stale-request supersede. Default = real UTC calendar days (byte-identical
// to the legacy per-day truncation everywhere it replaced).
//
// CHORA_DOSE_DAY_SECONDS shrinks the bucket for accelerated end-to-end
// testing (owner-directed 2026-07-11: e.g. 600 = a dose "day" every 10
// minutes so the warming→stirring→hatch arc proves out in one sitting).
// It is a TEST clock: unset it (or set 86400) after the window — per-day
// is the product semantic (ADR-227 D7/D13 anti-farming).
package doseclock

import (
	"fmt"
	"sync/atomic"
	"time"
)

// DefaultPeriod is the product dose day: one real UTC calendar day.
const DefaultPeriod = 24 * time.Hour

// period holds the active bucket length in nanoseconds; 0 = DefaultPeriod.
// Atomic so -race test runs and the serving goroutines stay clean.
var period atomic.Int64

// Init sets the bucket period from whole seconds (boot-time, fail-loud —
// callers must refuse to start on an invalid value, never fall back
// silently).
func Init(seconds int) error {
	if seconds <= 0 {
		return fmt.Errorf("doseclock: period must be positive seconds, got %d", seconds)
	}
	period.Store(int64(seconds) * int64(time.Second))
	return nil
}

// Reset restores the default period (test cleanup).
func Reset() { period.Store(0) }

// Period returns the active bucket length.
func Period() time.Duration {
	if p := period.Load(); p > 0 {
		return time.Duration(p)
	}
	return DefaultPeriod
}

// Bucket returns the start of t's dose day. At the default period this is
// t's UTC midnight — identical to the legacy utc-day truncation the
// campaign mechanics stamped and compared before the clock became tunable.
func Bucket(t time.Time) time.Time {
	return t.UTC().Truncate(Period())
}

// SameBucket reports whether a and b fall in the same dose day.
func SameBucket(a, b time.Time) bool {
	return Bucket(a).Equal(Bucket(b))
}

// Key returns a stable per-bucket seed key. At the default period it is
// the legacy "2006-01-02" date key, so dose seeds (and anything derived
// from them) are unchanged when the clock runs at product speed.
func Key(t time.Time) string {
	if Period() == DefaultPeriod {
		return t.UTC().Format("2006-01-02")
	}
	return Bucket(t).Format(time.RFC3339)
}
