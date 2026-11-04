package viewconfine

import (
	"errors"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/denialprobe"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func reasonOf(t *testing.T, err error) adapter.ReasonCode {
	t.Helper()
	var aErr *adapter.AdapterError
	if !errors.As(err, &aErr) {
		t.Fatalf("not a typed failure: %v", err)
	}
	return aErr.ReasonCode
}

func TestJudgeSeparatesAnUnusableBoundaryFromAnUnenforcedOne(t *testing.T) {
	worked := denialprobe.Result{Step: denialprobe.Step{Kind: denialprobe.Stat, Path: "/a"}}
	refused := denialprobe.Result{Step: denialprobe.Step{Kind: denialprobe.ReadFile, Path: "/b"}, Errno: int(syscall.EPERM)}
	if err := judge([]denialprobe.Result{worked, refused}, 1); err != nil {
		t.Fatalf("worked and refused: %v", err)
	}
	if reason := reasonOf(t, judge([]denialprobe.Result{refused, refused}, 1)); reason != adapter.ReasonConfinementUnavailable {
		t.Fatalf("a refused positive control = %s", reason)
	}
	if reason := reasonOf(t, judge([]denialprobe.Result{worked, worked}, 1)); reason != adapter.ReasonBackingConfinementFailed {
		t.Fatalf("an allowed denial = %s", reason)
	}
	ioFailure := denialprobe.Result{Step: denialprobe.Step{Kind: denialprobe.Connect, Port: 7}, Errno: int(syscall.EIO)}
	if reason := reasonOf(t, judge([]denialprobe.Result{worked, ioFailure}, 1)); reason != adapter.ReasonBackingConfinementFailed {
		t.Fatalf("an unrelated failure proves no denial: %s", reason)
	}
}
