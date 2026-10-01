package workercompletion_test

import (
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"testing"
)

func TestWorkerReportChecksProduceOneOccurrence(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	for _, tc := range []struct {
		name    string
		report  workercompletion.WorkerCompletionReport
		missing bool
		max     int
	}{
		{name: "valid", report: workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "Observed result"}},
		{name: "missing", missing: true},
		{name: "empty brief", report: workercompletion.WorkerCompletionReport{LegStatus: "partial"}},
		{name: "over budget", report: workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "☃🚀☃🚀"}, max: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := &oar.Rule{OAR: "1.0", ID: "REPORT_OCCURRENCE", Kind: oar.KindPolicy, Anchor: oar.AnchorWorkerReportCheck, Effect: oar.EffectAllow, Enforcement: "enforce", OnError: "fail_closed", OnFire: []oar.OnFireAction{oar.OnFireIncrementCounter}}
			pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
			pipeline.EnableAnchor(oar.AnchorWorkerReportCheck)
			result, err := workercompletion.EvaluateWorkerSummary(t.Context(), workercompletion.WorkerSummaryEvalInput{AgentType: "security-reviewer", ChildSessionID: t.Name(), Report: tc.report, MissingReport: tc.missing, MaxChars: tc.max, Pipeline: pipeline})
			testutil.FailErr(t, "evaluate report occurrence", err)
			// [OAR-FIRE-6] Side effects belong to exactly one report occurrence.
			if n := pipeline.Counters().Get(t.Name(), rule.Qualified(), oar.CounterFire); n != 1 {
				t.Fatalf("report evaluated %d times", n)
			}
			if result.Status != "complete" || result.HintCode != "" || result.PolicyFeedback() != nil {
				t.Fatalf("host invented a blocking policy result: %+v", result)
			}
			if tc.name != "valid" && result.Grounding.Traced {
				t.Fatal("allow policy fabricated valid grounding")
			}
		})
	}
}

func TestWorkerReportBudgetMeasuresUnprojectedUnicode(t *testing.T) {
	brief := strings.Repeat("🚀", 1500)
	input := workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{AgentType: "security-reviewer", ChildSessionID: t.Name(), MaxChars: 1400, Report: workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: brief}})
	result, err := workercompletion.EvaluateWorkerSummary(t.Context(), input)
	testutil.FailErr(t, "evaluate original report budget", err)
	if result.Status != "partial" || result.HintCode != "WORKER_SUMMARY_TOO_LONG" || result.Summary != brief {
		t.Fatalf("projection hid original budget violation: %+v", result)
	}
	if result.HintData["actual_chars"] != 1500 || result.HintData["max_chars"] != 1400 {
		t.Fatalf("budget facts = %+v", result.HintData)
	}
	if !strings.Contains(result.HintCopy["what"], "1500") || !strings.Contains(result.HintCopy["what"], "1400") {
		t.Fatalf("copy lost measured budget: %+v", result.HintCopy)
	}
}
