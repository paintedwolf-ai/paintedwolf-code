package session

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// resolverRecordingGuard observes the page-target resolver the manager installs.
type resolverRecordingGuard struct {
	*loopguard.MemoryDoomLoopGuard
	resolver func(sessionID, pageID string) string
}

func (g *resolverRecordingGuard) SetPageTargetResolver(fn func(sessionID, pageID string) string) {
	g.resolver = fn
	g.MemoryDoomLoopGuard.SetPageTargetResolver(fn)
}

func newResolverRecordingGuard() *resolverRecordingGuard {
	return &resolverRecordingGuard{MemoryDoomLoopGuard: loopguard.NewMemoryDoomLoopGuard()}
}

func TestDoomLoopPageResolverWiringOrder(t *testing.T) {
	for _, guardFirst := range []bool{true, false} {
		name := "pages first"
		if guardFirst {
			name = "guard first"
		}
		t.Run(name, func(t *testing.T) {
			manager := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
			guard := newResolverRecordingGuard()
			pages := pagesession.NewRegistry(pagesession.DefaultConfig())
			t.Cleanup(func() { pages.Close(t.Context()) })
			if guardFirst {
				manager.SetDoomLoopGuard(guard)
				manager.SetPageRegistry(pages)
			} else {
				manager.SetPageRegistry(pages)
				manager.SetDoomLoopGuard(guard)
			}
			if guard.resolver == nil {
				t.Fatal("page target resolver was not installed")
			}
			replacement := newResolverRecordingGuard()
			manager.SetDoomLoopGuard(replacement)
			if replacement.resolver == nil {
				t.Fatal("replacement guard lost page target resolution")
			}
		})
	}
}

func TestFirstChecklistPublicationResolvesMissingProgressWithoutGeneralAdvancement(t *testing.T) {
	manager, sessions := newTestManager(t)
	session, err := sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create coordinator", err)
	store := progress.NewMemoryStore()
	manager.SetProgressStore(store)
	guard := loopguard.NewMemoryDoomLoopGuard()
	manager.SetDoomLoopGuard(guard)
	args := map[string]any{"path": "main.go"}
	for n := range loopguard.DoomLoopMaxSameCodeRejects {
		testutil.FailErr(t, "retain missing checklist rejection", guard.RecordAttempt(t.Context(), session.ID, fmt.Sprint(n), "edit", args, &api.InvocationFailure{Code: "PROGRESS_MISSING"}, false))
	}
	allowed, _, _, err := guard.Check(t.Context(), session.ID, "missing", "edit", args, true, nil)
	testutil.FailErr(t, "check unresolved prerequisite", err)
	if allowed {
		t.Fatal("missing checklist released rejection")
	}
	update, err := progress.PrepareAuthorUpdate("- [ ] Deliver")
	testutil.FailErr(t, "prepare first checklist", err)
	result, err := update.Apply(t.Context(), store, session.ID, nil)
	testutil.FailErr(t, "publish first checklist", err)
	if result.AdvancesWorkflow() {
		t.Fatal("first checklist claimed deliverable advancement")
	}
	testutil.FailErr(t, "record neutral establishment", guard.RecordAttempt(t.Context(), session.ID, "checklist", "update_progress", map[string]any{"content": result.Snapshot.Content}, nil, false))
	allowed, count, _, err := guard.Check(t.Context(), session.ID, "recovered", "edit", args, true, nil)
	testutil.FailErr(t, "check stored prerequisite recovery", err)
	if !allowed || count != 0 {
		t.Fatalf("valid checklist did not release prior refusal: %v %d", allowed, count)
	}
	for n := range loopguard.DoomLoopMaxAttempts {
		testutil.FailErr(t, "record repeated edit", guard.RecordAttempt(t.Context(), session.ID, fmt.Sprint("edit-", n), "edit", args, nil, true))
	}
	allowed, _, _, err = guard.Check(t.Context(), session.ID, "repeat", "edit", args, true, nil)
	testutil.FailErr(t, "check ordinary repetition", err)
	if allowed {
		t.Fatal("prerequisite recovery disabled normal repetition tracking")
	}
}
