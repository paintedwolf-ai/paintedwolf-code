package session_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverlayPromoteConsultsLayeredConsumerDivergenceWithoutFullBaseline(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "source")
	mountDir := filepath.Join(tmp, "mount")
	backingDir := filepath.Join(tmp, "backing")
	for _, d := range []string{srcDir, mountDir, backingDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcDir, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-session-promote", BackingDir: backingDir, Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "main.go"}, []byte("package main\n// edited\n"), 0o644); err != nil {
		t.Fatalf("WriteFile main.go: %v", err)
	}
	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "feature.go"}, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile feature.go: %v", err)
	}

	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID:           "view-session-promote",
		Roots:            roots,
		MountedRootPaths: map[string]string{"code": mountDir},
		BackingDir:       backingDir,
		Core:             core,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	consumer.RegisterBranchEngine(mountDir, eng)
	t.Cleanup(func() { consumer.UnregisterBranchEngine(mountDir) })

	task := &api.WorkerTask{
		ID:            "job-layered-promote",
		Scope:         &api.TaskScope{Mode: api.TaskScopeModeWrite},
		WorkspaceRoot: mountDir,
		WorkspacePath: srcDir,
		// WorkspaceBaselinePath intentionally empty to prove layered O(changed) divergence is used.
	}
	rootRefs := []projectroot.RootRef{{ID: "code", Label: "code", Path: srcDir, IsPrimary: true}}

	changed, err := session.InspectOverlayChanges(ctx, task, rootRefs)
	if err != nil {
		t.Fatalf("InspectOverlayChanges: %v", err)
	}
	want := []string{"feature.go", "main.go"}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("InspectOverlayChanges = %v, want %v", changed, want)
	}

	div := session.OverlayWorkspaceDivergencePaths(task, rootRefs)
	if !reflect.DeepEqual(div, want) {
		t.Fatalf("OverlayWorkspaceDivergencePaths = %v, want %v", div, want)
	}
	if !session.OverlayAwaitingPromote(ctx, task) {
		t.Fatalf("expected OverlayAwaitingPromote=true when layered view has private changes")
	}
}
