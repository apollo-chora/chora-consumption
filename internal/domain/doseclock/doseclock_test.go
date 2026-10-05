// doseclock_test.go — the env-tunable dose-day bucket (CHO-2087 accelerated
// walk clock). The default MUST be byte-identical to the legacy utc-day
// truncation/keying it replaced; shrunken periods bucket sub-day.
package doseclock

import (
	"testing"
	"time"
)

var clkNow = time.Date(2026, 7, 9, 8, 14, 57, 0, time.UTC)

func TestDefault_MatchesLegacyUTCDay(t *testing.T) {
	Reset()
	wantBucket := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	if got := Bucket(clkNow); !got.Equal(wantBucket) {
		t.Fatalf("default Bucket = %v, want legacy UTC midnight %v", got, wantBucket)
	}
	if got := Bucket(clkNow); !got.Equal(clkNow.UTC().Truncate(24 * time.Hour)) {
		t.Fatalf("default Bucket must equal the legacy Truncate(24h) stamp")
	}
	if got := Key(clkNow); got != "2026-07-09" {
		t.Fatalf("default Key = %q, want legacy date key", got)
	}
	if Period() != DefaultPeriod {
		t.Fatalf("default Period = %v", Period())
	}
}

func TestInit_ShrinksBucketsAndKeys(t *testing.T) {
	if err := Init(600); err != nil {
		t.Fatalf("Init(600): %v", err)
	}
	t.Cleanup(Reset)
	if got := Bucket(clkNow); !got.Equal(time.Date(2026, 7, 9, 8, 10, 0, 0, time.UTC)) {
		t.Fatalf("600s Bucket = %v, want 08:10:00", got)
	}
	if !SameBucket(clkNow, clkNow.Add(4*time.Minute)) {
		t.Fatal("08:14 and 08:18 share the 08:10 bucket")
	}
	if SameBucket(clkNow, clkNow.Add(11*time.Minute)) {
		t.Fatal("08:14 and 08:25 are different buckets")
	}
	if Key(clkNow) == Key(clkNow.Add(11*time.Minute)) {
		t.Fatal("keys must differ across buckets")
	}
	if Key(clkNow) != Key(clkNow.Add(4*time.Minute)) {
		t.Fatal("keys must be stable within a bucket")
	}
}

func TestInit_RefusesNonPositive(t *testing.T) {
	t.Cleanup(Reset)
	for _, bad := range []int{0, -1, -86400} {
		if err := Init(bad); err == nil {
			t.Fatalf("Init(%d) must refuse", bad)
		}
	}
	if Period() != DefaultPeriod {
		t.Fatal("a refused Init must not disturb the period")
	}
}
