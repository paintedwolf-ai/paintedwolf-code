package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
)

type stubBranchWorkspace struct{}

func (stubBranchWorkspace) ValidateMeta(context.Context) error          { return nil }
func (stubBranchWorkspace) EnsureParents(context.Context, string) error { return nil }

func TestWorkerBranchSurveyAndDivergenceUseLayeredConsumerEngine(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "source", "code")
	mountDir := filepath.Join(tmp, "mount", "code")
	backingDir := filepath.Join(tmp, "backing")

	if err := os.MkdirAll(filepath.Join(srcDir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mountDir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir mount: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "pkg", "base.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("write base.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "pkg", "remove.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("write remove.go: %v", err)
	}

	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcDir, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-survey-wired", BackingDir: backingDir, Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/added.go"}, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("WriteFile added.go: %v", err)
	}
	if err := core.Unlink(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/remove.go"}, false); err != nil {
		t.Fatalf("Unlink remove.go: %v", err)
	}

	now := time.Unix(1710000000, 0).UTC()
	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID:           "view-survey-wired",
		Roots:            roots,
		MountedRootPaths: map[string]string{"code": mountDir},
		BackingDir:       backingDir,
		Core:             core,
		Index: consumer.SourceIndexFunc(func(_ context.Context, _ consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
			return consumer.SourceIndexResult{
				Entries: []consumer.SourceIndexEntry{
					{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/base.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 12, ModTime: now, ObservedAt: now},
					{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/remove.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 12, ModTime: now, ObservedAt: now},
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

	tctx := tools.ToolContext{
		Source: tools.InvocationSource{
			WorkerBranchRoot: mountDir,
			BranchWorkspace:  stubBranchWorkspace{},
		},
	}

	var walked []string
	ok, err := workerBranchSurveyWalk(ctx, tctx, mountDir, sandbox.SurveyOptions{}, func(se sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		walked = append(walked, se.Rel)
		return sandbox.SurveyContinue, nil
	})
	if err != nil || !ok {
		t.Fatalf("workerBranchSurveyWalk = (%v, %v), want (true, nil)", ok, err)
	}
	if !reflect.DeepEqual(walked, []string{"pkg/added.go", "pkg/base.go"}) {
		t.Fatalf("walked = %v, want [pkg/added.go pkg/base.go]", walked)
	}

	dirEntries, ok, err := workerBranchReadDir(ctx, tctx, filepath.Join(mountDir, "pkg"), sandbox.SurveyOptions{})
	if err != nil || !ok || len(dirEntries) != 2 {
		t.Fatalf("workerBranchReadDir = (%d, %v, %v), want 2 entries", len(dirEntries), ok, err)
	}

	if err := ensureBranchFileForRead(ctx, tctx, filepath.Join(mountDir, "pkg", "added.go")); err != nil {
		t.Fatalf("ensureBranchFileForRead added.go: %v", err)
	}
	if err := ensureBranchFileForRead(ctx, tctx, filepath.Join(srcDir, "pkg", "base.go")); !errors.Is(err, consumer.ErrDirectBackingAccessForbidden) {
		t.Fatalf("expected ErrDirectBackingAccessForbidden for direct source path, got %v", err)
	}

	divPaths, handled, err := workspace.OverlayWorkspaceDivergencePaths(ctx, mountDir)
	if err != nil || !handled || !reflect.DeepEqual(divPaths, []string{"pkg/added.go", "pkg/remove.go"}) {
		t.Fatalf("OverlayWorkspaceDivergencePaths = (%v, %v, %v)", divPaths, handled, err)
	}
}
