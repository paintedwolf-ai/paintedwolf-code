package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A grounding-rejected worker report is superseded in place: id, ord, and
// content are preserved and the row count is stable.
func TestSupersedeWorkerReportPreservesRowInPlace(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	reportID := "rejected-report"
	if err := store.AppendMessages(ctx, sess.ID, api.Message{
		ID:      reportID,
		Role:    api.MessageRoleAssistant,
		Content: `{"leg_status":"complete","brief":"ungrounded survey"}`,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	before, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before", err)
	var beforeOrd int64
	for _, m := range before {
		if m.ID == reportID {
			beforeOrd = m.Ord
		}
	}

	if err := mgr.Transcript.SupersedeWorkerReport(ctx, sess.ID, reportID); err != nil {
		testutil.FailErr(t, "SupersedeWorkerReport", err)
	}

	after, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	// Zero delete: the row count is stable across the retract.
	if len(after) != len(before) {
		t.Fatalf("message count = %d want %d (no physical delete on retract)", len(after), len(before))
	}
	var superseded *api.Message
	for i := range after {
		if after[i].ID == reportID {
			superseded = &after[i]
		}
	}
	if superseded == nil {
		t.Fatal("rejected report must remain in transcript (superseded, not deleted)")
	}
	if superseded.Kind != api.MessageKindSuperseded {
		t.Fatalf("kind = %q want superseded", superseded.Kind)
	}
	if superseded.Ord != beforeOrd {
		t.Fatalf("ord = %d want %d (ord preserved on supersede)", superseded.Ord, beforeOrd)
	}
	if superseded.Content == "" {
		t.Fatal("content must be preserved on supersede (row kept in search)")
	}
}
