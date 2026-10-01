package hostcmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
)

func TestExecFailureOfClassifiesRunErrors(t *testing.T) {
	cause := errors.New("disk full")
	for _, tc := range []struct {
		name string
		err  error
		want ExecFailureKind
	}{
		{"redirect commit", fmt.Errorf("%w: %w", exec.ErrRedirectCommit, cause), ExecFailureRedirectCommit},
		{"stage launch", fmt.Errorf("%w: %w", exec.ErrStageNotLaunched, cause), ExecFailureStageLaunch},
		{"other", cause, ExecFailureRun},
	} {
		got := ExecFailureOf(tc.err)
		if got == nil || got.Kind != tc.want || got.Detail != tc.err.Error() {
			t.Errorf("%s: ExecFailureOf = %+v, want kind %s", tc.name, got, tc.want)
		}
	}
	if got := ExecFailureOf(nil); got != nil {
		t.Errorf("ExecFailureOf(nil) = %+v, want nil", got)
	}
	if got := ExecFailureOf(&exec.TimeoutError{}); got != nil {
		t.Errorf("a timeout is reported by the termination reason, got failure %+v", got)
	}
}
