package coordinatorcontrol

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/session/promptsource"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPopulatePreparationFrameProjectsAuthoritativeRootCount(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "prep-frame.db")
	projects := project.NewSQLRegistry(sqlDB)
	sessions := store.NewSQL(sqlDB).Sessions

	rootDir := t.TempDir()
	rootedProj, err := projecttest.CreateWithRoot(ctx, projects, rootDir)
	testutil.FailErr(t, "create rooted project", err)

	rootlessProj, err := projects.Create(ctx, project.CreateParams{
		Name: "Rootless Project",
	})
	testutil.FailErr(t, "create rootless project", err)

	workspace := sessionscope.New(sessions)
	workspace.SetProjects(projects)

	svc := &Service{
		Context: &promptsource.Context{Workspace: workspace},
		Model:   &promptsource.Model{Workspace: workspace},
	}

	cases := []struct {
		name           string
		projectID      string
		overlayRoots   []string
		wantRootCount  int
		wantOverlayLen int
	}{
		{
			name:           "rooted_project",
			projectID:      rootedProj.ID,
			overlayRoots:   []string{rootDir},
			wantRootCount:  1,
			wantOverlayLen: 1,
		},
		{
			name:           "rootless_draft",
			projectID:      rootlessProj.ID,
			overlayRoots:   nil,
			wantRootCount:  0,
			wantOverlayLen: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{
				ID:        "sess-" + tc.name,
				ProjectID: tc.projectID,
				Posture:   api.SessionPostureBuild,
			}
			frame := inject.CoordinatorTurnFrame{}
			testutil.FailErr(t, "populatePreparationFrame", svc.populatePreparationFrame(ctx, sess, tc.overlayRoots, &frame))
			if frame.ProjectRootCount != tc.wantRootCount {
				t.Fatalf("frame.ProjectRootCount = %d, want %d", frame.ProjectRootCount, tc.wantRootCount)
			}
			if len(frame.OverlayRootPaths) != tc.wantOverlayLen {
				t.Fatalf("len(frame.OverlayRootPaths) = %d, want %d", len(frame.OverlayRootPaths), tc.wantOverlayLen)
			}
			if !slices.Equal(frame.OverlayRootPaths, tc.overlayRoots) {
				t.Fatalf("frame.OverlayRootPaths = %v, want %v", frame.OverlayRootPaths, tc.overlayRoots)
			}
		})
	}
}
