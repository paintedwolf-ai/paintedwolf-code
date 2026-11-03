package sourceapi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceBranchScopeAndWorkerDivergenceUseLayeredConsumerEngine(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "source", "code")
	mountDir := filepath.Join(tmp, "mount", "code")
	backingDir := filepath.Join(tmp, "backing")
	for _, d := range []string{srcDir, mountDir, backingDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcDir, "app.go"), []byte("package main\nconst V = 0\n"), 0o644); err != nil {
		t.Fatalf("write app.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "keep.go"), []byte("package main\nconst Keep = true\n"), 0o644); err != nil {
		t.Fatalf("write keep.go: %v", err)
	}

	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcDir, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-sourceapi-wired", BackingDir: backingDir, Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "app.go"}, []byte("package main\nconst V = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile v1: %v", err)
	}

	ckptStore, err := checkpoint.NewStore(filepath.Join(tmp, "checkpoints"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: core, Store: ckptStore, WorkerID: "w1", TaskID: "t1", ProjectID: "p1", SourceGeneration: "gen-1", SelectedRoots: roots,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if _, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "ckpt-1", Trigger: checkpoint.TriggerExplicitCapture}); err != nil {
		t.Fatalf("Publish ckpt-1: %v", err)
	}

	// Mutate live view after ckpt-1.
	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "live_only.go"}, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile live_only.go: %v", err)
	}

	now := time.Unix(1710000000, 0).UTC()
	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID:           "view-sourceapi-wired",
		Roots:            roots,
		MountedRootPaths: map[string]string{"code": mountDir},
		BackingDir:       backingDir,
		Core:             core,
		CheckpointStore:  ckptStore,
		Index: consumer.SourceIndexFunc(func(_ context.Context, _ consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
			return consumer.SourceIndexResult{
				Entries: []consumer.SourceIndexEntry{
					{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "app.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 25, ModTime: now, ObservedAt: now},
					{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "keep.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 31, ModTime: now, ObservedAt: now},
				},
				Coverage: consumer.CoverageComplete,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	consumer.RegisterBranchEngine(mountDir, eng)
	t.Cleanup(func() { consumer.UnregisterBranchEngine(mountDir) })

	proj := &project.Project{
		ID:    "p1",
		Roots: []project.Root{{ID: "code", Path: srcDir, Label: "code", IsPrimary: true}},
	}
	scopedProj, err := requestscope.SourceProjectInBranch(proj, mountDir)
	if err != nil {
		t.Fatalf("SourceProjectInBranch: %v", err)
	}
	if len(scopedProj.Roots) != 1 || scopedProj.Roots[0].Path != mountDir {
		t.Fatalf("expected scoped project root path %q, got %+v", mountDir, scopedProj.Roots)
	}

	ckptFiles := appendLayeredWorkerDivergenceChanges(ctx, mountDir, "ckpt-1", nil)
	wantCkpt := []wire.WorkerJobChangedFile{{RootID: "code", Path: "app.go", Op: "modify"}}
	if !reflect.DeepEqual(ckptFiles, wantCkpt) {
		t.Fatalf("ckpt-1 files = %+v, want %+v", ckptFiles, wantCkpt)
	}

	liveFiles := appendLayeredWorkerDivergenceChanges(ctx, mountDir, "", nil)
	wantLive := []wire.WorkerJobChangedFile{
		{RootID: "code", Path: "app.go", Op: "modify"},
		{RootID: "code", Path: "live_only.go", Op: "create"},
	}
	if !reflect.DeepEqual(liveFiles, wantLive) {
		t.Fatalf("live files = %+v, want %+v", liveFiles, wantLive)
	}
}
