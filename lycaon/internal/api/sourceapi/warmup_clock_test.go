package sourceapi

import (
	"testing"
	"time"
)

func TestWarmupRetryAfterMSGrowsWithElapsed(t *testing.T) {
	cases := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"first ask is brisk", 0, 150},
		{"still brisk while the work may be about to land", 400 * time.Millisecond, 150},
		{"proportional once the work has run a while", 2 * time.Second, 500},
		{"clamped for work that has proven long", time.Minute, 2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := warmupRetryAfterMS(tc.elapsed); got != tc.want {
				t.Fatalf("warmupRetryAfterMS(%s) = %d, want %d", tc.elapsed, got, tc.want)
			}
		})
	}
}

func TestWarmupClockBacksOffPerKeyAndResets(t *testing.T) {
	now := time.Unix(0, 0)
	clock := newWarmupClock()
	clock.now = func() time.Time { return now }

	if got := clock.warming("a"); got != 150 {
		t.Fatalf("first warming hint = %d, want 150", got)
	}
	// A second key keeps its own clock rather than inheriting the first's.
	now = now.Add(8 * time.Second)
	if got := clock.warming("b"); got != 150 {
		t.Fatalf("unrelated key first hint = %d, want 150", got)
	}
	if got := clock.warming("a"); got != 2000 {
		t.Fatalf("aged key hint = %d, want 2000", got)
	}

	// Once the work lands, the next warm-up starts brisk again.
	clock.ready("a")
	if got := clock.warming("a"); got != 150 {
		t.Fatalf("hint after ready = %d, want 150", got)
	}
}

func TestWarmupClockToleratesNilReceiver(t *testing.T) {
	// A handler built without the constructor answers the floor rather than
	// dereferencing nil or returning a zero that would spin a client.
	var clock *warmupClock
	if got := clock.warming("k"); got != 150 {
		t.Fatalf("unwired warming hint = %d, want 150", got)
	}
	clock.ready("k")
}
