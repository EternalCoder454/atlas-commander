package ui

import (
	"testing"
	"time"

	"atlas-commander/internal/fleet"
)

// The easing must start and end exactly on the endpoints and never overshoot,
// or a fade would flash or a slide would land off its row.
func TestEaseOutCubicHitsEndpointsAndStaysInRange(t *testing.T) {
	if got := easeOutCubic(0); got != 0 {
		t.Errorf("easeOutCubic(0) = %v, want 0", got)
	}
	if got := easeOutCubic(1); got != 1 {
		t.Errorf("easeOutCubic(1) = %v, want 1", got)
	}
	prev := 0.0
	for i := 1; i <= 100; i++ {
		v := easeOutCubic(float64(i) / 100)
		if v < prev || v > 1 {
			t.Fatalf("easeOutCubic(%d%%) = %v after %v, want rising and at most 1", i, v, prev)
		}
		prev = v
	}
	if got := easeOutCubic(2); got != 1 {
		t.Errorf("easeOutCubic(2) = %v, want 1 (clamped)", got)
	}
}

// Reversing a hover fade halfway must continue from the current value; a jump
// back to 0 or 1 would show as a flicker when the mouse skims across rows.
func TestRampRetargetContinuesFromCurrentValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATLAS_NO_ANIMATIONS", "")
	t0 := time.Unix(100, 0)
	up := ramp{}.retarget(t0, 1, 120)
	mid := t0.Add(60 * time.Millisecond)
	v := up.at(mid)
	if v <= 0 || v >= 1 {
		t.Fatalf("mid-fade value = %v, want strictly between 0 and 1", v)
	}
	down := up.retarget(mid, 0, 160)
	if got := down.at(mid); got != v {
		t.Errorf("value right after reversing = %v, want %v", got, v)
	}
	if !down.done(mid.Add(160 * time.Millisecond)) {
		t.Error("reversed ramp not done after its duration")
	}
	if got := down.at(mid.Add(time.Second)); got != 0 {
		t.Errorf("value long after = %v, want 0", got)
	}
}

// With animations off a ramp must be instant, as the desktop setting asks.
func TestRampRetargetIsInstantWhenAnimationsAreOff(t *testing.T) {
	t.Setenv("ATLAS_NO_ANIMATIONS", "1")
	now := time.Unix(100, 0)
	r := ramp{}.retarget(now, 1, 200)
	if !r.done(now) || r.at(now) != 1 {
		t.Errorf("ramp = %+v at start, want finished at 1", r)
	}
}

// The halo drives what the table and, later, the cards draw, so it must stay
// inside the dot's neighbourhood, fade out, and be absent for quiet statuses.
func TestPulseAtStaysBoundedAndOnlyForLiveStatuses(t *testing.T) {
	for _, st := range []fleet.Status{fleet.StatusRunning, fleet.StatusApproval} {
		for ms := 0; ms < 4000; ms += 7 {
			r, a := pulseAt(time.Unix(0, 0).Add(time.Duration(ms)*time.Millisecond), st)
			if r < pulseDotR || r > pulseDotR+8 || a < 0 || a > 0.7 {
				t.Fatalf("%s at %dms: radius %v alpha %v out of range", st, ms, r, a)
			}
		}
	}
	for _, st := range []fleet.Status{fleet.StatusIdle, fleet.StatusStopped, fleet.StatusError, fleet.StatusHeld} {
		if r, a := pulseAt(time.Unix(0, 12345), st); r != 0 || a != 0 {
			t.Errorf("%s pulses: radius %v alpha %v, want none", st, r, a)
		}
	}
	_, run := pulseAt(time.Unix(0, 0).Add(10*time.Millisecond), fleet.StatusRunning)
	_, appr := pulseAt(time.Unix(0, 0).Add(10*time.Millisecond), fleet.StatusApproval)
	if appr <= run {
		t.Errorf("approval alpha %v, want stronger than running %v", appr, run)
	}
}
