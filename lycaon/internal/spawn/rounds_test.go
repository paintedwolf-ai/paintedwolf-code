package spawn_test

import (
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
)

func TestThresholdCrossed(t *testing.T) {
	for _, tc := range []struct {
		name            string
		prev, cur, base int64
		want            bool
	}{
		{"below base", 0, 39, 40, false},
		{"reaches base", 39, 40, 40, true},
		{"jumps past base", 0, 70, 40, true},
		{"between doublings", 41, 79, 40, false},
		{"reaches first doubling", 79, 80, 40, true},
		{"skips several doublings", 41, 400, 40, true},
		{"no movement", 80, 80, 40, false},
		{"moves backwards", 90, 50, 40, false},
		{"zero base never fires", 0, 100, 0, false},
		{"negative base never fires", 0, 100, -1, false},
		{"near the integer limit", math.MaxInt64 - 1, math.MaxInt64, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spawn.ThresholdCrossed(tc.prev, tc.cur, tc.base); got != tc.want {
				t.Fatalf("ThresholdCrossed(%d, %d, %d) = %v, want %v", tc.prev, tc.cur, tc.base, got, tc.want)
			}
		})
	}
}

// Stepping a measurement upward fires once per threshold whatever the step
// size, as long as each step stays below the next doubling.
func TestThresholdCrossedFiresOncePerThreshold(t *testing.T) {
	const base, limit = 40, 40 * 64
	thresholds := 0
	for v := int64(base); v <= limit; v *= 2 {
		thresholds++
	}
	for _, step := range []int64{1, 3, 7, 39} {
		fired := 0
		for prev := int64(0); prev < limit; prev += step {
			if spawn.ThresholdCrossed(prev, min(prev+step, limit), base) {
				fired++
			}
		}
		if fired != thresholds {
			t.Fatalf("step %d fired %d times, want %d", step, fired, thresholds)
		}
	}
}

func TestDefaultRoundPolicyIsUsable(t *testing.T) {
	p := spawn.DefaultRoundPolicy()
	if p.ToollessResponseBound < 1 || p.ConsumptionNoticeModelCalls < 1 || p.QuietNoticeAfter <= 0 {
		t.Fatalf("default round policy = %+v", p)
	}
}
