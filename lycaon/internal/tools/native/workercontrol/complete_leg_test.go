package workercontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
)

// invokeCompleteLeg registers with the production decoder so these tests
// exercise the same decode the closeout extractor runs.
func invokeCompleteLeg(t *testing.T, args map[string]any, tctx tools.ToolContext) (string, error) {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterCompleteLegTool(reg, workercompletion.CompleteLegDecoder))
	def, ok := reg.Definition(workertools.CompleteLegTool)
	if !ok {
		t.Fatal("complete_leg missing")
	}
	return def.Handler(context.Background(), args, tctx)
}

// A session the user addressed has no leg to close. The refusal carries a Code
// so the model can branch to its ordinary closeout instead of retrying.
func TestCompleteLegRejectsAddressedSession(t *testing.T) {
	_, err := invokeCompleteLeg(t, map[string]any{
		"leg_status": "complete",
		"brief":      "done",
	}, tools.ToolContext{SessionID: "child"})
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COMPLETE_LEG_ADDRESSED_SESSION" {
		t.Fatalf("err = %v want COMPLETE_LEG_ADDRESSED_SESSION", err)
	}
}

func TestCompleteLegRequiresDecoder(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := native.RegisterCompleteLegTool(reg, nil); err == nil {
		t.Fatal("expected registration error without decoder")
	}
}

func TestCompleteLegAcksWithoutEchoingReport(t *testing.T) {
	out, err := invokeCompleteLeg(t, map[string]any{
		"leg_status":     "complete",
		"brief":          "surveyed auth",
		"objectives_met": []any{"mapped login"},
		"findings": []any{map[string]any{
			"path":    "internal/auth/token.go",
			"line":    12,
			"excerpt": "return nil",
		}},
	}, tools.ToolContext{SessionID: "child", ParentSessionID: "parent"})
	testutil.FailErr(t, "invoke", err)
	var ack struct {
		Recorded         bool   `json:"recorded"`
		LegStatus        string `json:"leg_status"`
		FindingsRecorded int    `json:"findings_recorded"`
		Brief            string `json:"brief"`
	}
	testutil.FailErr(t, "decode ack", json.Unmarshal([]byte(out), &ack))
	if !ack.Recorded || ack.LegStatus != "complete" || ack.FindingsRecorded != 1 {
		t.Fatalf("ack = %s", out)
	}
	// An echoed report would make the body large enough to compact; the
	// record is the ToolArgs snapshot.
	if ack.Brief != "" {
		t.Fatalf("ack must not echo the report, got %s", out)
	}
}

func TestCompleteLegAcceptedArgsAlwaysExtractable(t *testing.T) {
	args := map[string]any{
		"leg_status": "partial",
		"brief":      "half surveyed",
		"findings": []any{map[string]any{
			"path":    "internal/auth/token.go",
			"line":    12,
			"excerpt": "return nil",
		}},
	}
	_, err := invokeCompleteLeg(t, args, tools.ToolContext{SessionID: "child", ParentSessionID: "parent"})
	testutil.FailErr(t, "invoke", err)
	report, ok := workercompletion.ReportFromCompleteLegArgs(args)
	if !ok || report.LegStatus != "partial" || report.Brief != "half surveyed" || len(report.Findings) != 1 {
		t.Fatalf("accepted args must decode at finalize, report = %+v ok=%v", report, ok)
	}
}

func TestCompleteLegRejectsMissingStatus(t *testing.T) {
	_, err := invokeCompleteLeg(t, map[string]any{
		"brief": "done",
	}, tools.ToolContext{SessionID: "child", ParentSessionID: "parent"})
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COMPLETE_LEG_STATUS_REQUIRED" {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteLegRejectsAliasStatus(t *testing.T) {
	for _, status := range []string{"completed", "in_progress", "in progress"} {
		_, err := invokeCompleteLeg(t, map[string]any{
			"leg_status": status,
			"brief":      "done",
		}, tools.ToolContext{SessionID: "child", ParentSessionID: "parent"})
		var reject *tools.ToolReject
		if !errors.As(err, &reject) || reject.Code != "COMPLETE_LEG_STATUS_REQUIRED" {
			t.Fatalf("status %q err = %v", status, err)
		}
	}
}
