package testutil

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/runnerbudget"
)

func TestTimeoutUsesRunnerScale(t *testing.T) {
	t.Setenv(runnerbudget.TimeoutScaleEnv, "4")
	if got := Timeout(250 * time.Millisecond); got != time.Second {
		t.Fatalf("Timeout = %v, want 1s", got)
	}
}

func TestTimeoutIgnoresScaleOutsideRunnerRange(t *testing.T) {
	for _, value := range []string{"", "nope", "0", "-2", "5"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(runnerbudget.TimeoutScaleEnv, value)
			if got := Timeout(time.Second); got != time.Second {
				t.Fatalf("Timeout = %v, want 1s", got)
			}
		})
	}
}
