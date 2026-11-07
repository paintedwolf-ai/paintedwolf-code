package history

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stalePreparationHistory struct{ *store.Memory }

func (s stalePreparationHistory) GetCompactionView(context.Context, string) (*store.CompactionView, bool, error) {
	return &store.CompactionView{Generation: 1}, true, nil
}
func (s stalePreparationHistory) CompactionViewCurrent(context.Context, string, int64, string, int64) (bool, error) {
	return false, nil
}

func TestPreparationHistoryDoesNotScheduleStaleCompaction(t *testing.T) {
	service, data := newCompactionManager(t, compaction.DefaultCompactionConfig())
	sess, err := data.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append history", data.AppendMessages(t.Context(), sess.ID, api.Message{Role: api.MessageRoleUser, Content: "Known request"}))
	service.store = stalePreparationHistory{data}
	// Scheduling requires admission; projection must never enter it.
	service.Gate = nil
	rows, err := service.Project(t.Context(), sess)
	testutil.FailErr(t, "project stale view", err)
	if len(rows) != 1 || rows[0].Content != "Known request" || sess.CompactionGeneration != 0 {
		t.Fatalf("projection changed history or session: rows=%+v generation=%d", rows, sess.CompactionGeneration)
	}
}

func TestPreparationHistoryRequiresASession(t *testing.T) {
	service, _ := newCompactionManager(t, compaction.DefaultCompactionConfig())
	if _, err := service.Project(t.Context(), nil); err == nil {
		t.Fatal("projection accepted a missing session")
	}
}
