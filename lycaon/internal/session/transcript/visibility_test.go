package transcript

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStampUserMessageVisibilityMarksHostPromptsInternal(t *testing.T) {
	msg := api.Message{
		Role:   api.MessageRoleUser,
		Origin: api.MessageOriginHost,
		Kind:   api.MessageKindHostLoopWake,
	}
	StampUserVisibility(&msg)
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", msg.Visibility)
	}
}

func TestStampHostWaitOnlyAssistantHidesFromTranscript(t *testing.T) {
	msg := api.Message{
		Role:    api.MessageRoleAssistant,
		Content: "Waiting for workers…",
	}
	StampHostWaitOnlyAssistant(&msg, true, []string{"wait"})
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", msg.Visibility)
	}
}

func TestStampUserMessageVisibilityLeavesNormalUserTranscript(t *testing.T) {
	msg := api.Message{
		Role:    api.MessageRoleUser,
		Content: "Build a game",
	}
	StampUserVisibility(&msg)
	if msg.Visibility != "" {
		t.Fatalf("visibility = %q want empty transcript default", msg.Visibility)
	}
}

func TestAppendAndAppendContinuationStampCanonicalOrdinals(t *testing.T) {
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "fixture", Model: "large"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	svc := New(mem, nil)

	rows := []api.Message{{ID: "m1", Role: api.MessageRoleUser, Content: "first"}}
	testutil.FailErr(t, "append slice", svc.Append(t.Context(), sess.ID, rows...))
	if rows[0].Ord != 1 || rows[0].Seq != 1 {
		t.Fatalf("stamped slice row = (ord=%d, seq=%d), want (1, 1)", rows[0].Ord, rows[0].Seq)
	}

	single, err := svc.AppendMessage(t.Context(), sess.ID, api.Message{ID: "m2", Role: api.MessageRoleAssistant, Content: "second"})
	testutil.FailErr(t, "append single message", err)
	if single.Ord != 2 || single.Seq != 2 {
		t.Fatalf("stamped single row = (ord=%d, seq=%d), want (2, 2)", single.Ord, single.Seq)
	}

	cont, err := svc.AppendContinuation(t.Context(), sess.ID, promptinput.Input{SubmissionID: "sub-3", Text: "continue"})
	testutil.FailErr(t, "append continuation", err)
	if cont.Ord != 3 || cont.Seq != 3 {
		t.Fatalf("continuation row = (ord=%d, seq=%d), want (3, 3)", cont.Ord, cont.Seq)
	}

	retry, err := svc.AppendContinuation(t.Context(), sess.ID, promptinput.Input{SubmissionID: "sub-3", Text: "continue"})
	testutil.FailErr(t, "idempotent continuation retry", err)
	if retry.Ord != 3 || retry.Seq != 3 {
		t.Fatalf("idempotent continuation row = (ord=%d, seq=%d), want (3, 3)", retry.Ord, retry.Seq)
	}
}
