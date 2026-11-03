package consumer_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
)

func seedSourceTree(t *testing.T, rootDir string, files map[string]string) []consumer.SourceIndexEntry {
	t.Helper()
	now := time.Unix(1710000000, 0).UTC()
	var entries []consumer.SourceIndexEntry
	for rel, content := range files {
		abs := filepath.Join(rootDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		entries = append(entries, consumer.SourceIndexEntry{
			Path:       layeredfs.QualifiedPath{RootID: "code", Rel: rel},
			Kind:       layeredfs.EntryRegular,
			Mode:       0o644,
			Size:       int64(len(content)),
			ModTime:    now,
			ObservedAt: now,
			Freshness:  consumer.IndexFreshness{ObservedAt: now, SourceGeneration: 1, Coverage: consumer.CoverageComplete},
		})
	}
	return entries
}

func TestPrivateCreateDeleteRenameAndDirectoryReplacementConsistency(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcCode := filepath.Join(tmp, "source", "code")
	idxEntries := seedSourceTree(t, srcCode, map[string]string{
		"pkg/api/keep.go":     "package api\n// MATCH_TOKEN keep\n",
		"pkg/api/legacy.go":   "package api\n// MATCH_TOKEN legacy\n",
		"pkg/api/old_name.go": "package api\n// MATCH_TOKEN renamed\n",
		"pkg/sub/child.go":    "package sub\n// MATCH_TOKEN subchild\n",
	})

	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcCode, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-mutations", BackingDir: filepath.Join(tmp, "backing"), Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/new.go"}, []byte("package api\n// MATCH_TOKEN created\n"), 0o644); err != nil {
		t.Fatalf("WriteFile new.go: %v", err)
	}
	if err := core.Unlink(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/legacy.go"}, false); err != nil {
		t.Fatalf("Unlink legacy.go: %v", err)
	}
	if err := core.Rename(
		layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/old_name.go"},
		layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/renamed.go"},
	); err != nil {
		t.Fatalf("Rename old_name.go -> renamed.go: %v", err)
	}
	if err := core.Unlink(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/sub/child.go"}, false); err != nil {
		t.Fatalf("Unlink pkg/sub/child.go: %v", err)
	}
	if err := core.Unlink(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/sub"}, true); err != nil {
		t.Fatalf("Unlink dir pkg/sub: %v", err)
	}
	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/sub"}, []byte("replaced directory with file MATCH_TOKEN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile pkg/sub: %v", err)
	}

	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID:           "view-mutations",
		Roots:            roots,
		MountedRootPaths: map[string]string{"code": filepath.Join(tmp, "mount", "code")},
		BackingDir:       filepath.Join(tmp, "backing"),
		Core:             core,
		Index: consumer.SourceIndexFunc(func(_ context.Context, _ consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
			return consumer.SourceIndexResult{Entries: idxEntries, Coverage: consumer.CoverageComplete}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	orient, err := eng.BuildOrientationInventory(ctx, []string{"code"})
	if err != nil {
		t.Fatalf("BuildOrientationInventory: %v", err)
	}
	survey, err := eng.SurveyBranch(ctx, consumer.SurveyOptions{RootIDs: []string{"code"}})
	if err != nil {
		t.Fatalf("SurveyBranch: %v", err)
	}
	var gotPaths []string
	for _, e := range survey.Entries {
		gotPaths = append(gotPaths, e.Path.Rel)
	}
	wantPaths := []string{"pkg/api/keep.go", "pkg/api/new.go", "pkg/api/renamed.go", "pkg/sub"}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("survey paths = %v, want %v (orient len=%d)", gotPaths, wantPaths, len(orient.Entries))
	}

	apiDir, err := eng.ListDirectory(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api"}, "")
	if err != nil {
		t.Fatalf("ListDirectory pkg/api: %v", err)
	}
	var apiNames []string
	for _, e := range apiDir {
		apiNames = append(apiNames, e.Path.Rel)
	}
	if !reflect.DeepEqual(apiNames, []string{"pkg/api/keep.go", "pkg/api/new.go", "pkg/api/renamed.go"}) {
		t.Fatalf("ListDirectory pkg/api = %v", apiNames)
	}

	searchRes, err := eng.SearchWorkspace(ctx, consumer.SearchRequest{RootIDs: []string{"code"}, Query: "MATCH_TOKEN"})
	if err != nil {
		t.Fatalf("SearchWorkspace: %v", err)
	}
	var matchPaths []string
	for _, m := range searchRes.Matches {
		matchPaths = append(matchPaths, m.Path.Rel)
	}
	if !reflect.DeepEqual(matchPaths, wantPaths) {
		t.Fatalf("SearchWorkspace matches = %v, want %v", matchPaths, wantPaths)
	}

	if _, err := eng.ReadEvidence(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/legacy.go"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ErrNotExist for deleted legacy.go, got %v", err)
	}
	if _, err := eng.ReadEvidence(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/sub/child.go"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ErrNotExist for child under replaced dir pkg/sub, got %v", err)
	}
	div, err := eng.ComputeDivergence(ctx, "")
	if err != nil || !div.HasDivergence {
		t.Fatalf("ComputeDivergence failed: %v", err)
	}
}

func TestCheckpointSurveyAndInspectionStabilityDuringLiveEdits(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcCode := filepath.Join(tmp, "source", "code")
	idxEntries := seedSourceTree(t, srcCode, map[string]string{
		"pkg/api/handler.go": "package api\nconst Version = \"v0\"\n",
		"pkg/api/routes.go":  "package api\nconst Routes = \"stable\"\n",
	})
	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcCode, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-ckpt", BackingDir: filepath.Join(tmp, "backing"), Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	handlerQP := layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/handler.go"}
	routesQP := layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api/routes.go"}
	v1Body := []byte("package api\nconst Version = \"v1-checkpoint\"\n")
	if _, err := core.WriteFile(handlerQP, v1Body, 0o644); err != nil {
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

	if _, err := core.WriteFile(handlerQP, []byte("package api\nconst Version = \"v2-live\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile v2-live: %v", err)
	}
	if err := core.Unlink(routesQP, false); err != nil {
		t.Fatalf("Unlink routes.go: %v", err)
	}

	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID: "view-ckpt", Roots: roots, MountedRootPaths: map[string]string{"code": filepath.Join(tmp, "mount", "code")},
		Core: core, CheckpointStore: ckptStore,
		Index: consumer.SourceIndexFunc(func(_ context.Context, _ consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
			return consumer.SourceIndexResult{Entries: idxEntries, Coverage: consumer.CoverageComplete}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	ckptDir, err := eng.ListDirectory(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api"}, "ckpt-1")
	if err != nil || len(ckptDir) != 2 {
		t.Fatalf("expected 2 entries in ckpt-1 pkg/api, got %d (err=%v)", len(ckptDir), err)
	}
	liveDir, err := eng.ListDirectory(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/api"}, "")
	if err != nil || len(liveDir) != 1 {
		t.Fatalf("expected 1 entry in live pkg/api after unlink, got %d (err=%v)", len(liveDir), err)
	}

	ckptEv, err := eng.ReadCheckpointEntry(ctx, "ckpt-1", handlerQP, false)
	if err != nil || !bytes.Equal(ckptEv.Content, v1Body) || !ckptEv.StoredInCheckpoint || ckptEv.Source != consumer.AccessSourceCheckpointStore {
		t.Fatalf("unexpected ckpt-1 ReadCheckpointEntry result: %+v (err=%v)", ckptEv, err)
	}
	liveEv, err := eng.ReadEvidence(ctx, handlerQP)
	if err != nil || !bytes.Contains(liveEv.Content, []byte("v2-live")) {
		t.Fatalf("expected live ReadEvidence to return v2-live, got %q (err=%v)", string(liveEv.Content), err)
	}
}

func TestStructuralAccessBoundaryPreventsBackingShortcutAndAllowsBoundedTraversal(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcCode := filepath.Join(tmp, "backing", "code")
	_ = seedSourceTree(t, srcCode, map[string]string{
		"a/f1.go": "package a\n// TARGET\n",
		"b/f2.go": "package b\n// TARGET\n",
	})
	mountCode := filepath.Join(tmp, "mount", "code")
	_ = seedSourceTree(t, mountCode, map[string]string{
		"a/f1.go": "package a\n// TARGET\n",
		"b/f2.go": "package b\n// TARGET\n",
	})
	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcCode, IsPrimary: true}}
	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID: "view-boundary", Roots: roots, MountedRootPaths: map[string]string{"code": mountCode},
		BackingDir: filepath.Join(tmp, "backing"),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if err := eng.GuardLiveConsumerPath("code", filepath.Join(srcCode, "a", "f1.go")); !errors.Is(err, consumer.ErrDirectBackingAccessForbidden) {
		t.Fatalf("expected ErrDirectBackingAccessForbidden for lower source path, got %v", err)
	}
	if _, err := eng.ResolveMountedPath(layeredfs.QualifiedPath{RootID: "code", Rel: "../escape"}); !errors.Is(err, consumer.ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape for escaping relative path, got %v", err)
	}
	if !consumer.IsInternalStoragePath(".lycaon-work/upper") || consumer.IsInternalStoragePath(".gitignore") {
		t.Fatalf("unexpected IsInternalStoragePath classification")
	}

	bounded, err := eng.SearchWorkspace(ctx, consumer.SearchRequest{
		RootIDs: []string{"code"}, Query: "TARGET", AllowScopedTraversal: true, MaxDirsVisited: 1,
	})
	if err != nil || !bounded.IncompleteScope || bounded.Accounting.DirsVisited != 1 {
		t.Fatalf("expected bounded traversal with DirsVisited=1 and IncompleteScope=true, got %+v (err=%v)", bounded, err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	canceledRes, err := eng.SearchWorkspace(cancelCtx, consumer.SearchRequest{
		RootIDs: []string{"code"}, Query: "TARGET", AllowScopedTraversal: true,
	})
	if !errors.Is(err, context.Canceled) || !canceledRes.Canceled || !canceledRes.IncompleteScope {
		t.Fatalf("expected canceled traversal to set Canceled=true and IncompleteScope=true, got %+v (err=%v)", canceledRes, err)
	}
}

func TestMountedAndMaterializedAdaptersProduceIdenticalLogicalConsumerResults(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcCode := filepath.Join(tmp, "source", "code")
	idxEntries := seedSourceTree(t, srcCode, map[string]string{
		"pkg/svc.go": "package pkg\nfunc Serve() string { return \"ok\" }\n",
	})
	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcCode, IsPrimary: true}}

	runWithAdapter := func(name string, adp adapter.Adapter) (consumer.OrientationInventory, consumer.SearchResult, consumer.EvidenceReadResult, consumer.DivergenceSummary) {
		viewDir := filepath.Join(tmp, "views", name)
		view, err := adp.Prepare(ctx, adapter.PrepareRequest{
			ViewID:     "view-" + name,
			RuntimeDir: viewDir,
			BackingDir: filepath.Join(viewDir, "backing"),
			Grants:     []adapter.RootGrant{{RootID: "code", Label: "code", SourcePath: srcCode, IsPrimary: true}},
		})
		if err != nil {
			t.Fatalf("%s Prepare: %v", name, err)
		}
		t.Cleanup(func() { _, _ = adp.Release(ctx, view.ViewID) })

		eng, err := consumer.NewEngine(consumer.EngineConfig{
			ViewID: view.ViewID, Roots: roots, PreparedView: view, Core: view.Core, Adapter: adp,
			Index: consumer.SourceIndexFunc(func(_ context.Context, _ consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
				return consumer.SourceIndexResult{Entries: idxEntries, Coverage: consumer.CoverageComplete}, nil
			}),
		})
		if err != nil {
			t.Fatalf("%s NewEngine: %v", name, err)
		}
		if _, err := view.Core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/added.go"}, []byte("package pkg\n// Serve\n"), 0o644); err != nil {
			t.Fatalf("%s WriteFile: %v", name, err)
		}
		orient, err := eng.BuildOrientationInventory(ctx, []string{"code"})
		if err != nil {
			t.Fatalf("%s BuildOrientationInventory: %v", name, err)
		}
		search, err := eng.SearchWorkspace(ctx, consumer.SearchRequest{RootIDs: []string{"code"}, Query: "Serve"})
		if err != nil {
			t.Fatalf("%s SearchWorkspace: %v", name, err)
		}
		ev, err := eng.ReadEvidence(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/svc.go"})
		if err != nil {
			t.Fatalf("%s ReadEvidence: %v", name, err)
		}
		div, err := eng.ComputeDivergence(ctx, "")
		if err != nil {
			t.Fatalf("%s ComputeDivergence: %v", name, err)
		}
		return orient, search, ev, div
	}

	mountedAdp := adapter.NewFUSELinuxAdapter()
	copyAdp := adapter.NewEagerCopyAdapter(adapter.ReasonNone)

	mOrient, mSearch, mEv, mDiv := runWithAdapter("mounted", mountedAdp)
	cOrient, cSearch, cEv, cDiv := runWithAdapter("copy", copyAdp)

	if len(mOrient.Entries) != len(cOrient.Entries) || len(mSearch.Matches) != len(cSearch.Matches) || mEv.SHA256 != cEv.SHA256 || len(mDiv.Entries) != len(cDiv.Entries) {
		t.Fatalf("mismatch between mounted and eager-copy logical results")
	}
	if mOrient.VisibilityFacts.UntouchedVisibility != observation.VisibilityLive {
		t.Fatalf("expected mounted UntouchedVisibility=live, got %s", mOrient.VisibilityFacts.UntouchedVisibility)
	}
	if cOrient.VisibilityFacts.UntouchedVisibility != observation.VisibilityFixed {
		t.Fatalf("expected eager-copy UntouchedVisibility=fixed, got %s", cOrient.VisibilityFacts.UntouchedVisibility)
	}
}
