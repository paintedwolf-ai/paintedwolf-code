package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProvisionalAssistantStreamsInternalUntilCommit(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, messageID string, msg api.Message) error {
			if messageID != "a1" {
				t.Fatalf("messageID = %q want a1", messageID)
			}
			if msg.Visibility != api.MessageVisibilityTranscript {
				t.Fatalf("update visibility = %q want transcript", msg.Visibility)
			}
			return nil
		},
	})
	msg := newProvisionalAssistantMessage(api.Message{ID: "a1", Content: "draft summary"})
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", msg.Visibility)
	}
	committed, err := loop.commitProvisionalAssistantTurn(context.Background(), "s1", msg)
	testutil.FailErr(t, "loop.commitProvisionalAssistantTurn failed", err)
	if committed.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("committed visibility = %q want transcript", committed.Visibility)
	}
	if committed.Content != "draft summary" {
		t.Fatalf("committed content = %q", committed.Content)
	}
}

func TestCommitProvisionalAssistantInHistorySkipsAlreadyTranscript(t *testing.T) {
	updateCalls := 0
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, _ api.Message) error {
			updateCalls++
			return nil
		},
	})
	history := []api.Message{
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "ok", Visibility: api.MessageVisibilityTranscript},
	}
	out, err := loop.commitProvisionalAssistantInHistory(context.Background(), &api.Session{}, "s1", history, "", "", "a1")
	testutil.FailErr(t, "loop.commitProvisionalAssistantInHistory failed", err)
	if updateCalls != 0 {
		t.Fatalf("update calls = %d want 0 for already-transcript row", updateCalls)
	}
	if out[0].Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("visibility = %q", out[0].Visibility)
	}
}

func TestCommitProvisionalAssistantInHistoryAttachesGrounding(t *testing.T) {
	var committed api.Message
	grounding := &api.CitationGrounding{Traced: true}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
		ProseCitationGrounding: func(_ context.Context, _ *api.Session, _ []api.Message, _, prose, surfaceID string) *api.CitationGrounding {
			if surfaceID != "implement_synthesis" {
				t.Fatalf("surfaceID = %q want implement_synthesis", surfaceID)
			}
			if prose != "the fix landed" {
				t.Fatalf("prose = %q", prose)
			}
			return grounding
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "the fix landed", Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.commitProvisionalAssistantInHistory(
		context.Background(), &api.Session{}, "s1", history, "go", "implement_synthesis", "a1",
	)
	testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
	if committed.Grounding != grounding {
		t.Fatalf("committed grounding = %+v want attached", committed.Grounding)
	}
	if committed.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("committed visibility = %q want transcript", committed.Visibility)
	}
	if out[1].Grounding != grounding {
		t.Fatalf("history grounding not updated: %+v", out[1].Grounding)
	}
	if out[1].Kind != "" {
		t.Fatalf("closeout kind = %q want empty (answer bubble)", out[1].Kind)
	}
}

func TestCommitKeepsRawCloseoutEnvelopeInternal(t *testing.T) {
	// Incomplete closeout envelopes stay internal for retry.
	for _, raw := range []string{
		`{"synthesis":"The backend CLI is the ` + "`lycaon`" + ` binary, built from ` + "`lyca",
		"```json\n{\n  \"synthesis\": \"partial",
	} {
		var patched api.Message
		loop := NewPromptLoopForTest(PromptLoopDeps{
			UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
				patched = msg
				return nil
			},
		})
		history := []api.Message{
			{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
			{ID: "slot-1", Role: api.MessageRoleAssistant, Content: raw, Visibility: api.MessageVisibilityInternal},
		}
		out, err := loop.commitProvisionalAssistantInHistory(
			context.Background(), &api.Session{}, "s1", history, "go", "implement_investigate", "slot-1",
		)
		testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
		if patched.Visibility != api.MessageVisibilityInternal {
			t.Fatalf("raw envelope visibility = %q want internal (hidden)", patched.Visibility)
		}
		if out[1].Visibility != api.MessageVisibilityInternal {
			t.Fatalf("history row visibility = %q want internal", out[1].Visibility)
		}
	}
}

func TestCommitKeepsPoisonedSynthesisEnvelopeInternal(t *testing.T) {
	// The synthesis contains a nested completion envelope.
	inner := `{"synthesis":"## Report\n\nDone."}, "cited_evidence":[]}`
	raw, err := guidance.MarshalCoordinatorCompletionReport(guidance.CoordinatorCompletionReport{Synthesis: inner})
	testutil.FailErr(t, "MarshalCoordinatorCompletionReport", err)

	var patched api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: raw, Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.commitProvisionalAssistantInHistory(
		context.Background(), &api.Session{}, "s1", history, "go", "implement_investigate", "slot-1",
	)
	testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
	if patched.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("poisoned synthesis visibility = %q want internal", patched.Visibility)
	}
	// Grounding can expose an internal slot, so its wire content stays empty.
	if patched.Content != "" {
		t.Fatalf("wire body must not carry the machine envelope, got %q", patched.Content)
	}
	// Retry handling reads the envelope from internal history.
	if out[1].Content != raw {
		t.Fatalf("history must keep the machine envelope body, got %q", out[1].Content)
	}
}

func TestCommitStampsDraftOnInvestigateProseNotEnvelope(t *testing.T) {
	// Investigation prose commits as a visible draft.
	var committed api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "I'll scan the CLI entry point next.", Visibility: api.MessageVisibilityInternal},
	}
	_, err := loop.commitProvisionalAssistantInHistory(
		context.Background(), &api.Session{}, "s1", history, "go", "implement_investigate", "a1",
	)
	testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
	if committed.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("orchestration prose visibility = %q want transcript", committed.Visibility)
	}
	if committed.Kind != api.MessageKindDraft {
		t.Fatalf("orchestration prose kind = %q want draft", committed.Kind)
	}
}

func TestCommitProvisionalAssistantInHistoryStampsDraftOnOrchestrationSurface(t *testing.T) {
	var committed api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{
			ID:         "a1",
			Role:       api.MessageRoleAssistant,
			Content:    "The scan completed and the build baseline was collected.",
			Visibility: api.MessageVisibilityInternal,
		},
	}
	out, err := loop.commitProvisionalAssistantInHistory(
		context.Background(), &api.Session{}, "s1", history, "go", "implement_dispatch", "a1",
	)
	testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
	if committed.Kind != api.MessageKindDraft {
		t.Fatalf("committed kind = %q want draft", committed.Kind)
	}
	if out[1].Kind != api.MessageKindDraft {
		t.Fatalf("history kind = %q want draft", out[1].Kind)
	}
}

func TestCommitProvisionalAssistantInHistoryStampsDraftOnInvestigateOrchestration(t *testing.T) {
	var committed api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{
			ID:         "a1",
			Role:       api.MessageRoleAssistant,
			Content:    "The scan completed and the build baseline was collected.",
			Visibility: api.MessageVisibilityInternal,
		},
	}
	out, err := loop.commitProvisionalAssistantInHistory(
		context.Background(), &api.Session{}, "s1", history, "go", "implement_investigate", "a1",
	)
	testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
	if committed.Kind != api.MessageKindDraft {
		t.Fatalf("committed kind = %q want draft", committed.Kind)
	}
	if out[1].Kind != api.MessageKindDraft {
		t.Fatalf("history kind = %q want draft", out[1].Kind)
	}
}

func TestCommitProvisionalAssistantInHistoryDoesNotStampDraftOnWorkerChild(t *testing.T) {
	var committed api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
	})
	report := `{"leg_status":"complete","brief":"## Summary\n\nAll set."}`
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{
			ID:         "a1",
			Role:       api.MessageRoleAssistant,
			Content:    report,
			Visibility: api.MessageVisibilityInternal,
		},
	}
	child := &api.Session{ID: "child", ParentSessionID: "parent"}
	out, err := loop.commitProvisionalAssistantInHistory(
		context.Background(), child, "child", history, "go", "", "a1",
	)
	testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
	if committed.Kind != "" {
		t.Fatalf("committed kind = %q want empty for worker prose", committed.Kind)
	}
	if out[1].Kind != "" {
		t.Fatalf("history kind = %q want empty for worker prose", out[1].Kind)
	}
}

func TestCommitDoesNotStampDraftOnToolStep(t *testing.T) {
	for _, prose := range []string{"", "internal routing explanation"} {
		t.Run(prose, func(t *testing.T) {
			var committed api.Message
			loop := NewPromptLoopForTest(PromptLoopDeps{
				CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
				UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
					committed = msg
					return nil
				},
			})
			history := []api.Message{
				{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
				{
					ID:          "a1",
					Role:        api.MessageRoleAssistant,
					Visibility:  api.MessageVisibilityInternal,
					Content:     prose,
					DraftStatus: api.DraftStatusLive,
					ToolCalls:   []api.ToolCall{{ID: "call_1", Name: "read", Args: map[string]any{"path": "a.go"}}},
				},
			}
			out, err := loop.commitProvisionalAssistantInHistory(
				context.Background(), &api.Session{ID: "s1"}, "s1", history, "go", "implement_dispatch", "a1",
			)
			testutil.FailErr(t, "commitProvisionalAssistantInHistory", err)
			if committed.Kind != "" {
				t.Fatalf("committed kind = %q want empty for tool-call-only step", committed.Kind)
			}
			if committed.DraftStatus != api.DraftStatusCommitted {
				t.Fatalf("draft_status = %q want committed", committed.DraftStatus)
			}
			if out[1].Kind != "" {
				t.Fatalf("history kind = %q want empty", out[1].Kind)
			}
			if committed.Content != prose || out[1].Content != prose {
				t.Fatal("tool-step classification changed model history")
			}
		})
	}
}

func TestRejectBlockedAssistantTurnRetractsProvisionalRow(t *testing.T) {
	var patched api.Message
	var nudged []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: loadCoordinatorTestHintConfig(t),
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			nudged = append(nudged, msgs...)
			return nil
		},
		AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "bad synthesis", Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.rejectBlockedAssistantTurn(
		context.Background(),
		"s1",
		history,
		"a1",
		"",
		refusalForTest("Code: SYNTH_HANDLE_NOT_IN_LEGS — handle not in legs"),
		nil,
	)
	testutil.FailErr(t, "rejectBlockedAssistantTurn", err)
	if len(out) != 2 {
		t.Fatalf("history len = %d want 2 (user + nudge)", len(out))
	}
	if out[1].Role != api.MessageRoleUser || out[1].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("nudge row = %+v", out[1])
	}
	if patched.ID != "a1" || patched.DraftStatus != api.DraftStatusRejected {
		t.Fatalf("patched = %+v want id=a1 draft_status=rejected", patched)
	}
	if len(nudged) != 1 {
		t.Fatalf("nudged = %d want 1", len(nudged))
	}
}
