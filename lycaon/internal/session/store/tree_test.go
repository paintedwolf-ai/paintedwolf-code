package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type treeTestStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	CreateChild(context.Context, *api.Session, api.SpawnChildRequest) (*api.Session, error)
	SessionTreeMembers(context.Context, string) ([]SessionTreeMember, error)
}

func TestSessionTreeMembers(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s treeTestStore = NewMemory()
			if backend == "sqlite" {
				dir := t.TempDir()
				database := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
				testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
				s = NewSQL(database)
			}
			ctx := t.Context()
			root, err := s.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create root", err)
			_, err = s.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create unrelated session", err)
			child, err := s.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			grandchild, err := s.CreateChild(ctx, child, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create grandchild", err)
			members, err := s.SessionTreeMembers(ctx, root.ID)
			testutil.FailErr(t, "load tree", err)
			if len(members) != 3 {
				t.Fatalf("tree has %d members, want 3", len(members))
			}
			for i, want := range []*api.Session{grandchild, child, root} {
				if members[i].ID != want.ID || members[i].ProjectID != want.ProjectID || members[i].ParentSessionID != want.ParentSessionID {
					t.Fatalf("member %d = %+v, want identity of %+v", i, members[i], want)
				}
			}
			_, err = s.SessionTreeMembers(ctx, "missing")
			if !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("missing root error = %v", err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = s.SessionTreeMembers(canceled, root.ID)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled query error = %v", err)
			}
		})
	}
}

func TestOrderSessionTreeTerminatesOnCycle(t *testing.T) {
	members, err := orderSessionTree([]SessionTreeMember{
		{ID: "root", ParentSessionID: "child"},
		{ID: "child", ParentSessionID: "root"},
	}, "root")
	testutil.FailErr(t, "order cyclic tree", err)
	if len(members) != 2 || members[0].ID != "child" || members[1].ID != "root" {
		t.Fatalf("members = %+v", members)
	}
}
