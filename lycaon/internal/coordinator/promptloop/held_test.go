package promptloop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func heldTestLoop(held *heldcall.Registry) *PromptLoop {
	return NewPromptLoopForTest(PromptLoopDeps{HeldCalls: held, HeldCallBudget: 20 * time.Millisecond})
}

func heldTestCall(run func(context.Context) toolInvocation) heldToolCall {
	return heldToolCall{
		contract:   toolcontract.Contract{DetachAfterBudget: true},
		argsDigest: "digest",
		run:        run,
	}
}

func runHeldTestCall(loop *PromptLoop, call heldToolCall) toolInvocation {
	return toolBatch{loop}.runHeldToolCall(
		context.Background(),
		&api.Session{ID: "s1", ProjectID: "p1"},
		api.ToolCall{ID: "tc1", Name: "find"},
		tools.ToolContext{SessionID: "s1"},
		call,
	)
}

func TestDetachableCallOutlivingItsBudgetReturnsAHandleAndKeepsRunning(t *testing.T) {
	held := heldcall.New(nil, nil)
	release := make(chan struct{})
	call := heldTestCall(func(ctx context.Context) toolInvocation {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return toolInvocation{content: `{"results":["a.rs"]}`, invoked: true}
	})

	run := runHeldTestCall(heldTestLoop(held), call)

	var result heldcall.Result
	if err := json.Unmarshal([]byte(run.content), &result); err != nil {
		t.Fatalf("held result %q: %v", run.content, err)
	}
	if !result.Running || result.Handle != "held-1" || result.Tool != "find" {
		t.Fatalf("held result = %+v", result)
	}
	if run.captures.process == nil || !run.captures.process.Running || run.captures.process.Handle != "held-1" || run.captures.ownerRef != "held-1" {
		t.Fatalf("held call did not carry its handle on the result: %+v", run.captures)
	}
	if !run.invoked || !run.succeeded() {
		t.Fatalf("a handoff is a completed call: invoked=%t succeeded=%t", run.invoked, run.succeeded())
	}
	if known, running := held.State("s1", "held-1"); !known || !running {
		t.Fatalf("held state = known:%t running:%t", known, running)
	}

	close(release)
	deadline := time.After(2 * time.Second)
	for {
		status, err := held.Status("s1", "held-1")
		if err == nil && status.Settled != nil {
			if status.Settled.Content != `{"results":["a.rs"]}` {
				t.Fatalf("settled content = %q", status.Settled.Content)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the held call did not settle")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestDetachableCallInsideItsBudgetReturnsItsOwnResult(t *testing.T) {
	held := heldcall.New(nil, nil)
	call := heldTestCall(func(context.Context) toolInvocation {
		return toolInvocation{content: "listing", invoked: true}
	})

	run := runHeldTestCall(heldTestLoop(held), call)

	if run.content != "listing" || run.captures.process != nil {
		t.Fatalf("inline result = %+v", run)
	}
	if held.HasHandles("s1") {
		t.Fatal("an inline call left a handle behind")
	}
}

func TestHeldCallSettlesWithTheOutcomeOfItsOwnResult(t *testing.T) {
	held := heldcall.New(nil, nil)
	release := make(chan struct{})
	call := heldTestCall(func(context.Context) toolInvocation {
		<-release
		return toolInvocation{
			content: "Rejected: SURVEY_GLOB_INVALID",
			facts:   guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeRejected}.WithCode("SURVEY_GLOB_INVALID"),
		}
	})
	runHeldTestCall(heldTestLoop(held), call)
	close(release)

	deadline := time.After(2 * time.Second)
	for {
		status, err := held.Status("s1", "held-1")
		if err == nil && status.Settled != nil {
			if status.Settled.Outcome != api.ToolResultOutcomeRejected || len(status.Settled.Facts.Codes) != 1 {
				t.Fatalf("settled = %+v, want the rejection and its code", status.Settled)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the held call did not settle")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
