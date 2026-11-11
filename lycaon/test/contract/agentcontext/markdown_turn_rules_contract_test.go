package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// AC-COMPAT-09: over Markdown, each turn rule fires on its trigger through the real
// guards and stock policy. The closeout rules are driven through the real commit path
// in promptloop's TestMarkdownCloseoutRulesFireOnTheirTriggers; the report-phase and
// source-evidence rules through the session's own finish path.
func TestMarkdownTurnRulesFireOnTheirTriggers(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	sess := &api.Session{ID: "s1", AgentType: "coordinator", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Content: surface.HostLoopWakeSentinel}}
	for _, tc := range []struct {
		name, code, content, surfaceID string
		batch                          guard.BatchTurnGuard
	}{
		{name: "directives alone are no answer", code: guard.CoordinatorCloseoutNoAnswerCode, content: "[verify: targeted - Ran it]", surfaceID: spawn.SurfaceImplementSynthesis},
		{name: "bare JSON is no answer", code: guard.CoordinatorCloseoutNoAnswerCode, content: `{"synthesis":"Done."}`, surfaceID: spawn.SurfaceImplementSynthesis},
		{name: "a second report in the turn", code: guard.CoordinatorBatchAlreadyClosedCode, content: "Second report [read#1:2].", surfaceID: spawn.SurfaceImplementSynthesis, batch: guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true}},
		{name: "an echoed task block", code: guard.CoordinatorTaskEnvelopeEchoCode, content: `It returned <task job_id="j1" state="complete"></task>.`, surfaceID: "implement_routing"},
	} {
		t.Run(tc.code+"/"+tc.name, func(t *testing.T) {
			reject, blocked := guard.FormatHostNoToolTurnReject(sess, history, tc.content, nil, tc.surfaceID, true,
				surface.ImplementSessionState{}, formatter, tc.batch)
			if !blocked || !strings.Contains(reject, "Code: "+tc.code) {
				t.Fatalf("blocked = %v, reject = %q", blocked, reject)
			}
		})
	}

	t.Run(guard.WaitHiddenTextCode+"/an uncited update before wait", func(t *testing.T) {
		carrier := api.Message{ID: "c1", Role: api.MessageRoleAssistant, Content: "Waiting on the worker.", ToolCalls: []api.ToolCall{{ID: "w1", Name: "wait"}}}
		gc := oar.NewGuardContext()
		guard.ObserveWaitVisibility(sess, []api.Message{carrier}, "wait", true, gc)
		if !gc.Session.UnpublishedToolTextPending {
			t.Fatal("the unpublished update owes nothing")
		}
		quiet := oar.NewGuardContext()
		guard.ObserveWaitVisibility(sess, []api.Message{carrier, {ID: api.MidTurnNoteID("c1"), Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Waiting on the worker [task#1]."}}, "wait", true, quiet)
		if quiet.Session.UnpublishedToolTextPending {
			t.Fatal("a published update still owes")
		}
	})
}
