package hostcmd

import (
	"errors"

	"github.com/lycaon/lycaon/internal/exec"
)

// ExecFailureKind names why a run failed apart from its exit status.
type ExecFailureKind string

const (
	// ExecFailureRedirectCommit: redirected output did not land in its file.
	ExecFailureRedirectCommit ExecFailureKind = "redirect_commit"
	// ExecFailureStageLaunch: a sequenced stage's process never started.
	ExecFailureStageLaunch ExecFailureKind = "stage_launch"
	// ExecFailureRun: the run ended with an error of its own.
	ExecFailureRun ExecFailureKind = "run"
)

// ExecFailure is a run error that the exit status alone does not carry.
type ExecFailure struct {
	Kind   ExecFailureKind `json:"kind"`
	Detail string          `json:"detail"`
}

// ExecFailureOf classifies the error a finished run returned. A timeout is
// reported by the termination reason, so it is not a failure here.
func ExecFailureOf(err error) *ExecFailure {
	var timeout *exec.TimeoutError
	if err == nil || errors.As(err, &timeout) {
		return nil
	}
	kind := ExecFailureRun
	switch {
	case errors.Is(err, exec.ErrRedirectCommit):
		kind = ExecFailureRedirectCommit
	case errors.Is(err, exec.ErrStageNotLaunched):
		kind = ExecFailureStageLaunch
	}
	return &ExecFailure{Kind: kind, Detail: err.Error()}
}
