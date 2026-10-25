package chats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type retirementSelectionStore struct {
	Store
	pageError, treeError error
	pages                int
}

func (s *retirementSelectionStore) List(context.Context) ([]*wire.Session, error) {
	panic("retirement must not hydrate the global session list")
}
func (s *retirementSelectionStore) Get(context.Context, string) (*wire.Session, error) {
	panic("selection must not hydrate transcripts")
}
func (s *retirementSelectionStore) ListProjectSummaries(ctx context.Context, q store.SummaryQuery) (store.SummaryPage, error) {
	s.pages++
	if s.pageError != nil {
		return store.SummaryPage{}, s.pageError
	}
	q.Limit = 1
	return s.Store.ListProjectSummaries(ctx, q)
}
func (s *retirementSelectionStore) SessionTreeIDs(ctx context.Context, id string) ([]string, error) {
	if s.treeError != nil {
		return nil, s.treeError
	}
	return s.Store.SessionTreeIDs(ctx, id)
}
func TestProjectRetirementSelectionPagesBothArchiveStatesWithoutHydration(t *testing.T) {
	mem := store.NewMemory()
	for i := range 3 {
		root, err := mem.Create(t.Context(), wire.CreateSessionRequest{ProjectID: "target"}, "target")
		testutil.FailErr(t, "create root", err)
		if i == 0 {
			_, err = mem.CreateChild(t.Context(), root, wire.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			testutil.FailErr(t, "archive root", mem.UpdateSession(t.Context(), root.ID, func(s *wire.Session) { now := time.Now(); s.ArchivedAt = &now }))
		}
	}
	_, err := mem.Create(t.Context(), wire.CreateSessionRequest{ProjectID: "other"}, "other")
	testutil.FailErr(t, "create unrelated chat", err)
	selected := &retirementSelectionStore{Store: mem}
	plan, err := planProjectRetirement(t.Context(), selected, "target")
	testutil.FailErr(t, "select project chats", err)
	if len(plan.roots) != 3 || plan.total != 4 || selected.pages != 3 {
		t.Fatalf("selection = %+v, pages = %d", plan, selected.pages)
	}
	for _, root := range plan.roots {
		if root.session.ProjectID != "target" {
			t.Fatalf("unrelated project selected: %+v", root)
		}
	}
	for _, treeFailure := range []bool{false, true} {
		want := errors.New("selection unavailable")
		broken := &retirementSelectionStore{Store: mem}
		if treeFailure {
			broken.treeError = want
		} else {
			broken.pageError = want
		}
		if _, err := planProjectRetirement(t.Context(), broken, "target"); !errors.Is(err, want) {
			t.Fatalf("selection error = %v", err)
		}
	}
}
