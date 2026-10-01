package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type compactionInterleavedStore struct {
	Store
	beforeSuffix func()
}

func (s *compactionInterleavedStore) GetMessagesAfterOrd(ctx context.Context, id string, after int64, limit int) ([]api.Message, error) {
	s.beforeSuffix()
	return s.Store.GetMessagesAfterOrd(ctx, id, after, limit)
}

func TestCompactionSuffixCannotMaskConcurrentPrefixEdit(t *testing.T) {
	mgr, mem := newCompactionManager(t, compaction.DefaultCompactionConfig())
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append prefix", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "original", Role: api.MessageRoleAssistant, Content: "original fact"}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read prefix", err)
	last := rows[len(rows)-1]
	testutil.FailErr(t, "publish initial view", mem.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
		Generation: 1, CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: last.Seq,
		Messages: []api.Message{{ID: "summary", Role: api.MessageRoleAssistant, Content: "original fact summarized"}},
	}))
	sess, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read generation", err)
	mgr.store = &compactionInterleavedStore{Store: mem, beforeSuffix: func() {
		last.Content = "corrected fact"
		_, err := mem.UpdateMessage(t.Context(), sess.ID, last.ID, last)
		testutil.FailErr(t, "edit covered prefix", err)
		testutil.FailErr(t, "append newer suffix", mem.AppendMessages(t.Context(), sess.ID,
			api.Message{ID: "suffix", Role: api.MessageRoleUser, Content: "continue with the correction"}))
	}}
	if _, _, _, _, _, err := mgr.loadCompactionPage(t.Context(), sess); err == nil {
		t.Fatal("accepted stale prefix because the newer suffix advanced the source sequence")
	}
	current, err := mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read final generation", err)
	if current.CompactionGeneration != 1 {
		t.Fatalf("stale snapshot advanced generation to %d", current.CompactionGeneration)
	}
}
