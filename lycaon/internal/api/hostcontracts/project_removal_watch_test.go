package hostcontracts

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type removalWatchRegistry struct {
	project.Registry
	beforeDelete func()
	failure      error
}

func (r removalWatchRegistry) Delete(ctx context.Context, id string) error {
	r.beforeDelete()
	if r.failure != nil {
		return r.failure
	}
	return r.Registry.Delete(ctx, id)
}

func TestProjectRemovalStopsWatchBeforeDeletionAndRestartsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			registry := project.NewMemoryRegistry()
			p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: root}}})
			testutil.FailErr(t, "create project", err)
			stopped := make(chan bool, 1)
			wrapped := removalWatchRegistry{Registry: registry, beforeDelete: func() { stopped <- !repochange.Coverage(root).Watching }}
			if fail {
				wrapped.failure = errors.New("deletion failed")
			}
			server := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{
				Core:    hostapi.CoreDependencies{Store: sessionstore.NewMemory(), Projects: wrapped},
				Storage: hostapi.StorageDependencies{DataDir: t.TempDir(), WorkerSeedRoot: t.TempDir()},
			}), nil, hostapi.TestAPIToken)
			sourcefeed.EnsureProjectWatch(t.Context(), nil, p.ID, "", []sourcefeed.RootSpec{{ID: p.Roots[0].ID, WorkspaceID: p.WorkspaceID(), Path: root}}, nil)
			t.Cleanup(func() { sourcefeed.StopProjectWatch(context.Background(), p.ID) })
			if !repochange.Coverage(root).Watching {
				t.Fatal("fixture did not bind a watch")
			}
			_, err = server.Admin.Project.Removal.Owner.Remove(t.Context(), p.ID, wire.ProjectRemovalRequest{OperationID: uuid.NewString()})
			testutil.FailErr(t, "accept removal", err)
			contractfixture.DrainBackground(t, server)
			select {
			case wasStopped := <-stopped:
				if !wasStopped {
					t.Fatal("project rows deleted while the source watch was bound")
				}
			default:
				t.Fatal("removal never reached project deletion")
			}
			if watching := repochange.Coverage(root).Watching; watching != fail {
				t.Fatalf("watching after removal = %v, want %v", watching, fail)
			}
		})
	}
}
