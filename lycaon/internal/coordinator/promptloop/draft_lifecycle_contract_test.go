package promptloop

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// inPlaceStore is a tiny same-package stand-in for session.Store so contract
// tests can assert id/ord stability without importing session (import cycle).
type inPlaceStore struct {
	mu   sync.Mutex
	rows []api.Message
	next int64
}

func (s *inPlaceStore) Append(msg api.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	msg.Ord = s.next
	s.rows = append(s.rows, msg)
}

func (s *inPlaceStore) Update(_ context.Context, _, id string, msg api.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID != id {
			continue
		}
		ord := s.rows[i].Ord
		msg.Ord = ord
		s.rows[i] = msg
		return nil
	}
	return nil
}

func (s *inPlaceStore) All() []api.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.Message, len(s.rows))
	copy(out, s.rows)
	return out
}

func (s *inPlaceStore) Find(id string) api.Message {
	for _, m := range s.All() {
		if m.ID == id {
			return m
		}
	}
	return api.Message{}
}

// TestProvisionalCommitPreservesIDAndOrd is the store contract: provisional
// → committed is an in-place UpdateMessage on the same id; ord never changes.
func TestProvisionalCommitPreservesIDAndOrd(t *testing.T) {
	store := &inPlaceStore{}
	store.Append(api.Message{
		ID:          "slot-1",
		Role:        api.MessageRoleAssistant,
		Content:     "streaming answer",
		Visibility:  api.MessageVisibilityInternal,
		DraftStatus: api.DraftStatusLive,
	})
	before := store.Find("slot-1")
	ordBefore := before.Ord

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
			UpdateMessage:      store.Update,
		},
	})
	committed, err := loop.Projection.commitGuardedAssistantTurn(
		context.Background(), &api.Session{ID: "s1"}, "s1", store.All(), "go", "", before,
	)
	testutil.FailErr(t, "commitGuardedAssistantTurn", err)
	if committed.ID != "slot-1" {
		t.Fatalf("committed id = %q want slot-1", committed.ID)
	}

	after := store.All()
	if len(after) != 1 {
		t.Fatalf("row count = %d want 1 (in-place, never delete+append)", len(after))
	}
	if after[0].ID != "slot-1" || after[0].Ord != ordBefore {
		t.Fatalf("after = {id=%s ord=%d} want {id=slot-1 ord=%d}", after[0].ID, after[0].Ord, ordBefore)
	}
	if after[0].DraftStatus != api.DraftStatusCommitted {
		t.Fatalf("draft_status = %q want committed", after[0].DraftStatus)
	}
	if after[0].Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("visibility = %q want transcript", after[0].Visibility)
	}
}

// A completed multi-step turn leaves no live draft rows.
func TestMultiStepTurnLeavesZeroLiveRows(t *testing.T) {
	store := &inPlaceStore{}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
			UpdateMessage:      store.Update,
		},
	})

	steps := []api.Message{
		{
			ID: "step-prose-tools", Role: api.MessageRoleAssistant, Content: "looking around",
			DraftStatus: api.DraftStatusLive, Visibility: api.MessageVisibilityInternal,
			ToolCalls: []api.ToolCall{{ID: "c1", Name: "read"}},
		},
		{
			ID: "step-tools-only", Role: api.MessageRoleAssistant, Content: "",
			DraftStatus: api.DraftStatusLive, Visibility: api.MessageVisibilityInternal,
			ToolCalls: []api.ToolCall{{ID: "c2", Name: "grep"}},
		},
		{
			ID: "step-prose-final", Role: api.MessageRoleAssistant, Content: "done",
			DraftStatus: api.DraftStatusLive, Visibility: api.MessageVisibilityInternal,
		},
	}
	for _, step := range steps {
		store.Append(step)
		live := store.Find(step.ID)
		_, err := loop.Projection.commitGuardedAssistantTurn(
			context.Background(), &api.Session{ID: "s1"}, "s1", store.All(), "go", "", live,
		)
		testutil.FailErr(t, "settle "+step.ID, err)
	}

	final := store.All()
	liveCount := 0
	for _, m := range final {
		if m.DraftStatus == api.DraftStatusLive {
			liveCount++
		}
	}
	if liveCount != 0 {
		t.Fatalf("live rows after turn = %d want 0", liveCount)
	}
	if len(final) != 3 {
		t.Fatalf("row count = %d want 3 (all steps retained in place)", len(final))
	}
}

// TestNonSlotRejectNeverDeletesRow asserts the non-slot reject path patches in
// place (id/ord retained) and never drops the row.
func TestNonSlotRejectNeverDeletesRow(t *testing.T) {
	store := &inPlaceStore{}
	store.Append(api.Message{
		ID: "a1", Role: api.MessageRoleAssistant, Content: "worker bad",
		Visibility: api.MessageVisibilityInternal, DraftStatus: api.DraftStatusLive,
	})
	before := store.Find("a1")
	ordBefore := before.Ord

	var updateCalls atomic.Int32
	var recordedBody, recordedCode string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			UpdateMessage: func(ctx context.Context, sessionID, id string, msg api.Message) error {
				updateCalls.Add(1)
				return store.Update(ctx, sessionID, id, msg)
			},
			AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error { return nil },
			AppendDraftVersion: func(_ context.Context, _, _, body, code string) (int, error) {
				recordedBody, recordedCode = body, code
				return 1, nil
			},
		},
	})
	history := []api.Message{before}
	_, err := loop.Tools.rejectBlockedAssistantTurn(
		context.Background(), "s1", history, "a1", "", refusalForTest("Code: TEST"), nil,
	)
	testutil.FailErr(t, "rejectBlockedAssistantTurn", err)

	// After the retraction this is the only surviving copy.
	if recordedBody != "worker bad" {
		t.Errorf("recorded body = %q, want the rejected turn's body", recordedBody)
	}
	if recordedCode != "TEST" {
		t.Errorf("recorded outcome code = %q, want TEST", recordedCode)
	}

	after := store.All()
	if len(after) != 1 {
		t.Fatalf("row count = %d want 1 (no physical delete)", len(after))
	}
	if after[0].ID != "a1" || after[0].Ord != ordBefore {
		t.Fatalf("after = {id=%s ord=%d} want {id=a1 ord=%d}", after[0].ID, after[0].Ord, ordBefore)
	}
	if after[0].DraftStatus != api.DraftStatusRejected {
		t.Fatalf("draft_status = %q want rejected", after[0].DraftStatus)
	}
	if updateCalls.Load() < 1 {
		t.Fatal("expected UpdateMessage for in-place supersede")
	}
}

// TestWithdrawPreservesIDAndOrd keeps the draft slot id/ord when a live draft is withdrawn.
func TestWithdrawPreservesIDAndOrd(t *testing.T) {
	store := &inPlaceStore{}
	store.Append(api.Message{
		ID: "slot-1", Role: api.MessageRoleAssistant, Content: "never committed",
		Visibility: api.MessageVisibilityInternal, DraftStatus: api.DraftStatusLive, Kind: api.MessageKindDraft,
	})
	ordBefore := store.Find("slot-1").Ord

	st := &promptLoopTurnState{
		draftSlotID:          "slot-1",
		draftSlotAppended:    true,
		lastAssistantContent: "never committed",
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
			UpdateMessage:      store.Update,
		},
	})
	testutil.FailErr(t, "withdraw", loop.Nudges.maybeWithdrawCoordinatorDraft(
		context.Background(), &api.Session{ID: "s1"}, "s1", st,
	))

	after := store.All()
	if len(after) != 1 || after[0].ID != "slot-1" || after[0].Ord != ordBefore {
		t.Fatalf("after = %+v want id=slot-1 ord=%d", after, ordBefore)
	}
	if after[0].DraftStatus != api.DraftStatusWithdrawn {
		t.Fatalf("draft_status = %q want withdrawn", after[0].DraftStatus)
	}
}
