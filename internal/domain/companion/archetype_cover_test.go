package companion

import "testing"

// TestClamp_LowerBound exercises the v < lo branch of clamp, which the
// allocation path (always additive from a 50 baseline) never reaches. clamp
// is the single guard keeping a Stat within [1,100]; a regression dropping
// the lower clamp would let a stat fall to 0 or negative.
func TestClamp_LowerBound(t *testing.T) {
	cases := []struct {
		name            string
		v, lo, hi, want int
	}{
		{"below lower bound", -5, 1, 100, 1},
		{"at lower bound", 1, 1, 100, 1},
		{"above upper bound", 250, 1, 100, 100},
		{"at upper bound", 100, 1, 100, 100},
		{"within range", 42, 1, 100, 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clamp(c.v, c.lo, c.hi); got != c.want {
				t.Fatalf("clamp(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

// TestStatAllocation_Validate covers the negative-component and sum branches
// of Validate directly (the Summon path covers the happy case but the
// table here pins both rejection arms and the nil-error pass).
func TestStatAllocation_Validate(t *testing.T) {
	cases := []struct {
		name    string
		alloc   StatAllocation
		wantErr bool
	}{
		{"valid sum 3", StatAllocation{Curiosity: 1, Focus: 1, Recall: 1}, false},
		{"valid all on one", StatAllocation{Social: 3}, false},
		{"negative component", StatAllocation{Curiosity: -1, Focus: 4}, true},
		{"sum below 3", StatAllocation{Curiosity: 1, Focus: 1}, true},
		{"sum above 3", StatAllocation{Curiosity: 2, Focus: 2}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.alloc.Validate()
			if c.wantErr && err == nil {
				t.Fatalf("Validate(%+v) = nil, want error", c.alloc)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("Validate(%+v) = %v, want nil", c.alloc, err)
			}
		})
	}
}
