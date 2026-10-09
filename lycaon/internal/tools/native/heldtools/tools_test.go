package heldtools

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMain(m *testing.M) { testutil.VerifyNoLeaks(m, func() {}) }

func holdCall(t *testing.T, registry *heldcall.Registry, fn heldcall.Func) string {
	t.Helper()
	outcome, err := registry.Run(t.Context(), heldcall.Spec{
		SessionID: "s1", ToolCallID: "tc1", Tool: "find", ArgsDigest: "d", Budget: 5 * time.Millisecond,
	}, fn)
	if err != nil || outcome.Handle == "" {
		t.Fatalf("hold = %+v, %v", outcome, err)
	}
	return outcome.Handle
}

func run(t *testing.T, tool interface {
	Run(context.Context, map[string]any, tools.ToolContext) (string, error)
}, args map[string]any) (map[string]any, error) {
	t.Helper()
	out, err := tool.Run(t.Context(), args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s1"},
		Effects:  tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return decoded, nil
}

func TestCallResultReportsRunningThenTheSettledResult(t *testing.T) {
	registry := heldcall.New(nil, nil)
	release := make(chan struct{})
	handle := holdCall(t, registry, func(ctx context.Context) heldcall.Settled {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return heldcall.Settled{Content: `{"results":["a.rs"]}`, Outcome: api.ToolResultOutcomeCompleted}
	})
	tool := &ResultTool{Registry: registry}

	running, err := run(t, tool, map[string]any{"handle": handle})
	if err != nil {
		testutil.FailErr(t, "read while running", err)
	}
	if running["state"] != "running" || running["tool"] != "find" || running["result"] != nil {
		t.Fatalf("running payload = %v", running)
	}

	close(release)
	settled, err := run(t, tool, map[string]any{"handle": handle, "wait_ms": 2000})
	if err != nil {
		testutil.FailErr(t, "read after settle", err)
	}
	if settled["state"] != "settled" || settled["outcome"] != "completed" {
		t.Fatalf("settled payload = %v", settled)
	}
	result, ok := settled["result"].(map[string]any)
	if !ok || result["results"] == nil {
		t.Fatalf("a JSON result must embed as itself: %v", settled["result"])
	}
}

func TestCallResultCarriesTextResultsAndFailureCodes(t *testing.T) {
	registry := heldcall.New(nil, nil)
	handle := holdCall(t, registry, func(context.Context) heldcall.Settled {
		time.Sleep(20 * time.Millisecond)
		facts := guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeRejected}.WithCode("SURVEY_GLOB_INVALID")
		return heldcall.Settled{Content: "Rejected: SURVEY_GLOB_INVALID", Outcome: api.ToolResultOutcomeRejected, Facts: facts}
	})
	payload, err := run(t, &ResultTool{Registry: registry}, map[string]any{"handle": handle, "wait_ms": 2000})
	if err != nil {
		testutil.FailErr(t, "read", err)
	}
	if payload["outcome"] != "rejected" || payload["result"] != "Rejected: SURVEY_GLOB_INVALID" {
		t.Fatalf("payload = %v", payload)
	}
	codes, _ := payload["codes"].([]any)
	if len(codes) != 1 || codes[0] != "SURVEY_GLOB_INVALID" {
		t.Fatalf("codes = %v", payload["codes"])
	}
}

func TestUnknownHandleRejectsWithAStructuredCode(t *testing.T) {
	registry := heldcall.New(nil, nil)
	for name, tool := range map[string]interface {
		Run(context.Context, map[string]any, tools.ToolContext) (string, error)
	}{"held_result": &ResultTool{Registry: registry}, "held_stop": &StopTool{Registry: registry}} {
		_, err := run(t, tool, map[string]any{"handle": "held-9"})
		reject := toolrejection.AsToolReject(err)
		if reject == nil || reject.Code != "HELD_CALL_NOT_FOUND" || reject.Data["handle"] != "held-9" {
			t.Fatalf("%s error = %v", name, err)
		}
	}
}

func TestCallStopCancelsARunningCallAndReportsItSettled(t *testing.T) {
	registry := heldcall.New(nil, nil)
	handle := holdCall(t, registry, func(ctx context.Context) heldcall.Settled {
		<-ctx.Done()
		return heldcall.Settled{Content: "canceled", Outcome: api.ToolResultOutcomeError}
	})
	stop, err := run(t, &StopTool{Registry: registry}, map[string]any{"handle": handle})
	if err != nil {
		testutil.FailErr(t, "stop", err)
	}
	if stop["stop_requested"] != true || stop["running"] != true {
		t.Fatalf("stop payload = %v", stop)
	}
	settled, err := run(t, &ResultTool{Registry: registry}, map[string]any{"handle": handle, "wait_ms": 2000})
	if err != nil {
		testutil.FailErr(t, "read", err)
	}
	if settled["state"] != "settled" || settled["stopped"] != true {
		t.Fatalf("after stop = %v", settled)
	}
	again, err := run(t, &StopTool{Registry: registry}, map[string]any{"handle": handle})
	if err != nil {
		testutil.FailErr(t, "stop again", err)
	}
	if again["stop_requested"] != false || again["running"] != false {
		t.Fatalf("a settled call must not report a stop request: %v", again)
	}
}
