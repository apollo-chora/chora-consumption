package campaignquestion

import "testing"

func TestBudgetFor_TapStartsAtThree(t *testing.T) {
	b := BudgetFor(OriginTap, 0)
	if b.Cap != DefaultDailyTapCap {
		t.Errorf("Cap = %d, want %d", b.Cap, DefaultDailyTapCap)
	}
	if b.Used != 0 || b.Remaining != DefaultDailyTapCap {
		t.Errorf("used/remaining = %d/%d, want 0/%d", b.Used, b.Remaining, DefaultDailyTapCap)
	}
	if b.Exhausted {
		t.Error("a fresh tap budget must not be exhausted")
	}
}

func TestBudgetFor_MarchStartsAtOne(t *testing.T) {
	// The two budgets are independent by design: a spent march never blocks a
	// tap. Pinning the march cap here catches anyone collapsing them later.
	b := BudgetFor(OriginMarch, 0)
	if b.Cap != DefaultDailyMarchCap {
		t.Errorf("Cap = %d, want %d", b.Cap, DefaultDailyMarchCap)
	}
	if b.Remaining != DefaultDailyMarchCap {
		t.Errorf("Remaining = %d, want %d", b.Remaining, DefaultDailyMarchCap)
	}
}

func TestBudgetFor_CountsDown(t *testing.T) {
	b := BudgetFor(OriginTap, 2)
	if b.Used != 2 {
		t.Errorf("Used = %d, want 2", b.Used)
	}
	if b.Remaining != 1 {
		t.Errorf("Remaining = %d, want 1", b.Remaining)
	}
	if b.Exhausted {
		t.Error("one left is not exhausted")
	}
}

func TestBudgetFor_ExhaustedAtTheCap(t *testing.T) {
	b := BudgetFor(OriginTap, DefaultDailyTapCap)
	if b.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0", b.Remaining)
	}
	if !b.Exhausted {
		t.Error("spending the whole budget must report Exhausted")
	}
}

func TestBudgetFor_RemainingNeverGoesNegative(t *testing.T) {
	// Used CAN exceed the cap: the caps are domain constants the owner may
	// tune down, and a learner who already spent four taps under the old cap
	// still has those rows. A negative remaining would render as "-1 left" on
	// the card and would break any client that treats it as a count.
	b := BudgetFor(OriginTap, DefaultDailyTapCap+5)
	if b.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0 (clamped)", b.Remaining)
	}
	if !b.Exhausted {
		t.Error("over-spend must still report Exhausted")
	}
	if b.Used != DefaultDailyTapCap+5 {
		t.Errorf("Used = %d, want the true count %d; clamping Remaining must not falsify Used",
			b.Used, DefaultDailyTapCap+5)
	}
}

func TestBudgetFor_NegativeUsedIsTreatedAsZero(t *testing.T) {
	// A negative count is a caller or repository bug. Treating it as zero
	// keeps the card renderable; carrying it through would show more taps
	// remaining than the cap allows.
	b := BudgetFor(OriginTap, -3)
	if b.Used != 0 {
		t.Errorf("Used = %d, want 0", b.Used)
	}
	if b.Remaining != DefaultDailyTapCap {
		t.Errorf("Remaining = %d, want %d", b.Remaining, DefaultDailyTapCap)
	}
}

func TestBudgetFor_UnknownOriginFallsBackToTheConservativeCap(t *testing.T) {
	// DailyCapFor already treats an unknown origin as march, the smaller
	// budget. The projection must not widen that: showing three taps for an
	// origin the enforcement path caps at one would promise something the
	// next request refuses.
	b := BudgetFor(RequestOrigin("nonsense"), 0)
	if b.Cap != DefaultDailyMarchCap {
		t.Errorf("Cap = %d, want the conservative %d", b.Cap, DefaultDailyMarchCap)
	}
}

func TestBudgetFor_AgreesWithCanRequestToday(t *testing.T) {
	// The projection and the enforcement must never disagree. If they can,
	// the card says "1 left" and the next tap is refused, which reads as a
	// broken button. Table-driven across the whole boundary.
	for _, origin := range []RequestOrigin{OriginMarch, OriginTap} {
		for used := 0; used <= DailyCapFor(origin)+2; used++ {
			b := BudgetFor(origin, used)
			allowed := CanRequestToday(origin, used)
			if allowed != (b.Remaining > 0) {
				t.Errorf("origin %s used %d: CanRequestToday=%v but Remaining=%d",
					origin, used, allowed, b.Remaining)
			}
			if allowed == b.Exhausted {
				t.Errorf("origin %s used %d: Exhausted=%v contradicts CanRequestToday=%v",
					origin, used, b.Exhausted, allowed)
			}
		}
	}
}

func TestBudgetFor_CarriesItsOrigin(t *testing.T) {
	if got := BudgetFor(OriginTap, 0).Origin; got != OriginTap {
		t.Errorf("Origin = %q, want %q", got, OriginTap)
	}
}
