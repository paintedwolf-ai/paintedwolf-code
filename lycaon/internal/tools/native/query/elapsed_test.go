package query

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

func TestQueryElapsedBudgetSurvivesInputBoundariesAndPreservesOutput(t *testing.T) {
	h := newHarness(t, nil)
	var stdout bytes.Buffer
	inv := &invocation{ctx: t.Context(), owners: h.owners}
	inv.out = &output{ctx: inv.ctx, w: &stdout}
	program, err := nativejq.CompileProgram(".", nativejq.ProgramOptions{})
	testutil.FailErr(t, "nativejq.CompileProgram failed", err)
	run := &jqRun{inv: inv, program: program, emitter: &emitter{inv: inv}}
	if more, err := run.evaluate(1, true); !more || err != nil || stdout.String() != "1\n" || run.elapsed <= 0 {
		t.Fatalf("first evaluation %t %v output %q elapsed %v", more, err, stdout.String(), run.elapsed)
	}
	maximum := h.owners.limit("query_elapsed_ms")
	// Inject the existing cumulative counter immediately below its boundary.
	// A fresh per-input deadline would incorrectly admit this second input.
	run.elapsed = time.Duration(maximum)*time.Millisecond - time.Nanosecond
	more, err := run.evaluate(2, true)
	reject := toolrejection.AsToolReject(err)
	if more || reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != "query_elapsed_ms" || reject.Data["maximum"] != maximum {
		t.Fatalf("elapsed refusal %t %v", more, err)
	}
	observed := int64((run.elapsed + time.Millisecond - 1) / time.Millisecond)
	if reject.Data["observed"] != observed || observed <= maximum || !bytes.HasPrefix(stdout.Bytes(), []byte("1\n")) {
		t.Fatalf("actual elapsed %+v time %v output %q", reject.Data, run.elapsed, stdout.String())
	}
}

func TestQueryElapsedBudgetKeepsCallerCancellation(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout bytes.Buffer
	inv := &invocation{ctx: ctx, owners: h.owners}
	inv.out = &output{ctx: ctx, w: &stdout}
	program, err := nativejq.CompileProgram(".", nativejq.ProgramOptions{})
	testutil.FailErr(t, "nativejq.CompileProgram failed", err)
	run := &jqRun{inv: inv, program: program, emitter: &emitter{inv: inv}}
	_, err = run.evaluate(1, true)
	if !errors.Is(err, context.Canceled) || toolrejection.AsToolReject(err) != nil || stdout.Len() != 0 {
		t.Fatalf("caller cancellation %v output %q", err, stdout.String())
	}
}
