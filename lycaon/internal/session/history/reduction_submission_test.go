package history

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type publicationOnlyStore struct {
	Store
	reads   int
	release <-chan struct{}
}

func (s *publicationOnlyStore) Get(ctx context.Context, id string) (*api.Session, error) {
	s.reads++
	if s.reads > 1 {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Store.Get(ctx, id)
}

func TestPreparedPublicationDoesNotSubmitWorkUnderItsAdmissionGate(t *testing.T) {
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append source", mem.AppendMessages(t.Context(), sess.ID, api.Message{ID: "source", Role: "assistant", Content: "original source"}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read source", err)
	release := make(chan struct{})
	st := &publicationOnlyStore{Store: mem, release: release}
	svc := &Service{store: st, Gate: lifecycle.New(mem), Runner: NewRunner(), Compactor: compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), nil)}
	defer func() {
		close(release)
		svc.Runner.Stop()
		testutil.FailErr(t, "drain background work", svc.Runner.WaitContext(t.Context()))
	}()
	snapshot := store.CompactionView{CoveredThroughOrd: rows[0].Ord, CoveredThroughID: rows[0].ID, SourceSeq: rows[0].Seq}
	var published bool
	testutil.FailErr(t, "publish under admission", svc.Gate.WithSessionTreeAdmission(t.Context(), sess.ID, func() error {
		var publishErr error
		published, publishErr = svc.publishReductionSynthesis(t.Context(), compaction.SessionInfo{ID: sess.ID}, rows, snapshot,
			[]compaction.ContextMessage{{ID: "summary", Role: "assistant", Content: "short"}},
			compaction.CompactionReport{SessionCompacted: true, TokensBefore: 100, TokensAfter: 2})
		return publishErr
	}))
	svc.Runner.mu.Lock()
	submitted := len(svc.Runner.running)
	svc.Runner.mu.Unlock()
	if !published || submitted != 0 {
		t.Fatalf("publication submitted work before releasing admission: published=%v submitted=%d", published, submitted)
	}
}
