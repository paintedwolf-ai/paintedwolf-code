package workercontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
)

func invokeCompleteLeg(t *testing.T, args map[string]any, tctx tools.ToolContext) (string, error) {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterCompleteLegTool(reg, func(_ context.Context, args map[string]any, tc tools.ToolContext) (*api.WorkerReportAcceptance, error) {
		report, err := workercompletion.DecodeCompleteLegArgs(args)
		if err != nil {
			return nil, err
		}
		return &api.WorkerReportAcceptance{WorkerID: tc.Identity.WorkerJobID, Report: workercompletion.ReportWire(report), Grounding: &api.CitationGrounding{}}, nil
	}))
	def, ok := reg.Definition(workertools.CompleteLegTool)
	if !ok {
		t.Fatal("complete_leg missing")
	}
	tctx.Identity.WorkerJobID = "job"
	if tctx.Effects.Out == nil {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
	}
	return def.Handler(context.Background(), args, tctx)
}

func TestCompleteLegRejectsAddressedSession(t *testing.T) {
	_, err := invokeCompleteLeg(t, map[string]any{
		"leg_status": "complete",
		"brief":      "done",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child"},
	})
	var reject *toolrejection.ToolReject
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
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child",
			ParentSessionID: "parent"},
	})
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
	// The accepted report is retained separately from the acknowledgement.
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
	_, err := invokeCompleteLeg(t, args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child",
			ParentSessionID: "parent"},
	})
	testutil.FailErr(t, "invoke", err)
	report, ok := workercompletion.ReportFromCompleteLegArgs(args)
	if !ok || report.LegStatus != "partial" || report.Brief != "half surveyed" || len(report.Findings) != 1 {
		t.Fatalf("accepted args must decode at finalize, report = %+v ok=%v", report, ok)
	}
}

func TestCompleteLegRejectsMissingStatus(t *testing.T) {
	_, err := invokeCompleteLeg(t, map[string]any{
		"brief": "done",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child",
			ParentSessionID: "parent"},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COMPLETE_LEG_STATUS_REQUIRED" {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteLegRejectsAliasStatus(t *testing.T) {
	for _, status := range []string{"completed", "in_progress", "in progress"} {
		_, err := invokeCompleteLeg(t, map[string]any{
			"leg_status": status,
			"brief":      "done",
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "child",
				ParentSessionID: "parent"},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "COMPLETE_LEG_STATUS_REQUIRED" {
			t.Fatalf("status %q err = %v", status, err)
		}
	}
}

func TestCompleteLegPreservesHostContractRejection(t *testing.T) {
	rejection := &toolrejection.ToolReject{Code: "COMPLETE_LEG_COVERAGE_INVALID", Data: map[string]any{"detail": "missing assessment"}}
	handler := workertools.CompleteLegHandler(func(_ context.Context, _ map[string]any, tc tools.ToolContext) (*api.WorkerReportAcceptance, error) {
		if tc.Identity.WorkerJobID != "job" {
			t.Fatal("lost worker identity")
		}
		return nil, rejection
	})
	body, err := handler(t.Context(), map[string]any{"leg_status": "complete"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child",
			ParentSessionID: "parent",
			WorkerJobID:     "job"},
	})
	if body != "" || !errors.Is(err, rejection) {
		t.Fatalf("host rejection became ack: %q, %v", body, err)
	}
}

func TestCompleteLegRequiresCapturableJobAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name          string
		job           string
		capture       bool
		omitReport    bool
		omitGrounding bool
		wantAccepted  bool
	}{
		{name: "accepted", job: "job", capture: true, wantAccepted: true},
		{name: "wrong job", job: "other", capture: true},
		{name: "no job", capture: true},
		{name: "no capture", job: "job"},
		{name: "no report", job: "job", capture: true, omitReport: true},
		{name: "no grounding", job: "job", capture: true, omitGrounding: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := &api.WorkerReportAcceptance{WorkerID: tc.job, Report: &api.WorkerCompletionReport{LegStatus: "partial", Brief: "Accepted"}, Grounding: &api.CitationGrounding{}}
			if tc.omitReport {
				receipt.Report = nil
			}
			if tc.omitGrounding {
				receipt.Grounding = nil
			}
			handler := workertools.CompleteLegHandler(func(context.Context, map[string]any, tools.ToolContext) (*api.WorkerReportAcceptance, error) {
				return receipt, nil
			})
			tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "child", ParentSessionID: "parent", WorkerJobID: "job"}}
			if tc.capture {
				tctx.Effects.Out = &tools.ToolInvocationOut{}
			}
			body, err := handler(t.Context(), map[string]any{"leg_status": "complete", "brief": "Unvalidated input"}, tctx)
			if tc.wantAccepted {
				testutil.FailErr(t, "capture accepted report", err)
				if body == "" || tctx.Effects.Out.WorkerReport != receipt {
					t.Fatal("lost host acceptance")
				}
			} else if err == nil || body != "" || (tctx.Effects.Out != nil && tctx.Effects.Out.WorkerReport != nil) {
				t.Fatalf("uncapturable acceptance acknowledged: body=%q err=%v", body, err)
			}
		})
	}
}
