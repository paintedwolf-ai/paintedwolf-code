package project_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

type feedCap struct {
	mu          sync.Mutex
	evs         []api.SourceChange
	projectID   string
	workspaceID string
}

func (h *feedCap) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	h.mu.Lock()
	h.evs = append(h.evs, ev.Changes...)
	h.projectID = ev.ProjectID
	h.workspaceID = ev.WorkspaceID
	h.mu.Unlock()
	return nil
}

func (h *feedCap) scope() (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.projectID, h.workspaceID
}

func (h *feedCap) SourceChangedTx(ctx context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	return h.SourceChanged(ctx, ev)
}

func (h *feedCap) Deliver() {}

func (h *feedCap) take() []api.SourceChange {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]api.SourceChange(nil), h.evs...)
	h.evs = nil
	return out
}

func TestHostWriteDoorsEmitOnce(t *testing.T) {
	hub := &feedCap{}
	t.Cleanup(sourcefeed.Bind(hub))

	dir := t.TempDir()
	p := &project.Project{
		ID:    "proj-feed",
		Roots: []project.Root{{ID: "r1", Path: dir}},
	}
	seed := filepath.Join(dir, "seed.go")
	testutil.FailErr(t, "seed", os.WriteFile(seed, []byte("package seed\n"), 0o644))
	base := textfile.SHA256([]byte("package seed\n"))
	service := project.NewSourceMutationService(nil, nil)
	trash := t.TempDir()
	service.SetTrashMover(func(_ context.Context, path string) error {
		return os.Rename(path, filepath.Join(trash, uuid.NewString()))
	})

	cases := []struct {
		door sourcefeed.EmitDoor
		run  func(t *testing.T)
		want api.SourceChangeOp
		orig api.SourceChangeOrigin
	}{
		{
			door: sourcefeed.DoorUserPut,
			want: api.SourceChangeOpWrite,
			orig: api.SourceChangeOriginUser,
			run: func(t *testing.T) {
				_, err := service.Write(context.Background(), uuid.NewString(), p, project.SourceWriteRequest{
					Path: "seed.go", RootID: "r1", Content: "package seed\n//x\n",
					Encoding: textfile.UTF8, BaseSHA256: base,
				})
				testutil.FailErr(t, "put", err)
				base = textfile.SHA256([]byte("package seed\n//x\n"))
			},
		},
		{
			door: sourcefeed.DoorUserPost,
			want: api.SourceChangeOpCreate,
			orig: api.SourceChangeOriginUser,
			run: func(t *testing.T) {
				_, err := service.Create(context.Background(), uuid.NewString(), p, project.SourceEntryCreateRequest{
					Path: "new.go", Kind: project.SourceEntryFile, RootID: "r1",
				})
				testutil.FailErr(t, "post", err)
			},
		},
		{
			door: sourcefeed.DoorRename,
			want: api.SourceChangeOpRename,
			orig: api.SourceChangeOriginUser,
			run: func(t *testing.T) {
				_, err := service.Rename(context.Background(), uuid.NewString(), p, project.SourceRenameRequest{
					RootID: "r1", From: "new.go", To: "renamed.go",
				})
				testutil.FailErr(t, "rename", err)
			},
		},
		{
			door: sourcefeed.DoorCopy,
			want: api.SourceChangeOpCreate,
			orig: api.SourceChangeOriginUser,
			run: func(t *testing.T) {
				_, err := service.Copy(context.Background(), uuid.NewString(), p, project.SourceCopyRequest{
					RootID: "r1", From: "renamed.go", To: "copied.go",
				})
				testutil.FailErr(t, "copy", err)
			},
		},
		{
			door: sourcefeed.DoorDelete,
			want: api.SourceChangeOpDelete,
			orig: api.SourceChangeOriginUser,
			run: func(t *testing.T) {
				err := service.Delete(context.Background(), uuid.NewString(), p, project.SourceDeleteRequest{
					RootID: "r1", Path: "copied.go", Recursive: false,
				})
				testutil.FailErr(t, "delete", err)
			},
		},
		{
			door: sourcefeed.DoorReplaceApply,
			want: api.SourceChangeOpWrite,
			orig: api.SourceChangeOriginUser,
			run: func(t *testing.T) {
				_, err := service.BatchWrite(context.Background(), uuid.NewString(), p, project.SourceBatchWriteRequest{
					Input: "replace", Prepare: func() (project.SourceBatchWritePlan, error) {
						return project.SourceBatchWritePlan{Writes: []project.SourceWriteRequest{{
							RootID: "r1", Path: "seed.go", Content: "package seed2\n//x\n",
							Encoding: textfile.UTF8, BaseSHA256: base,
						}}}, nil
					},
				})
				testutil.FailErr(t, "replace", err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.door), func(t *testing.T) {
			_ = hub.take()
			tc.run(t)
			got := hub.take()
			if len(got) != 1 {
				t.Fatalf("events=%d want exactly 1: %+v", len(got), got)
			}
			if got[0].Op != tc.want || got[0].Origin != tc.orig {
				t.Fatalf("op/origin=%s/%s want %s/%s", got[0].Op, got[0].Origin, tc.want, tc.orig)
			}
			projectID, workspaceID := hub.scope()
			if projectID != p.ID || workspaceID != p.WorkspaceID() {
				t.Fatalf("event scope = (%q, %q)", projectID, workspaceID)
			}
			if tc.want != api.SourceChangeOpDelete && (got[0].IsDir == nil || *got[0].IsDir) {
				t.Fatalf("file mutation is_dir = %v", got[0].IsDir)
			}
			if tc.want == api.SourceChangeOpRename && got[0].FromPath == "" {
				t.Fatal("rename must carry from_path")
			}
		})
	}
}
