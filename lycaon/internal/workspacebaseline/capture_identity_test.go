package workspacebaseline_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func TestCaptureRequiresTopologyBeforePublishing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roots []projectroot.RootRef
	}{
		{name: "missing"},
		{name: "missing identity", roots: []projectroot.RootRef{{IsPrimary: true}}},
		{name: "missing primary", roots: []projectroot.RootRef{{ID: "root"}}},
		{name: "duplicate", roots: []projectroot.RootRef{{ID: "root", IsPrimary: true}, {ID: "root"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, branch := t.TempDir(), t.TempDir()
			store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "manifests"))
			ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(tc.roots, branch))
			if err == nil || ref != "" {
				t.Fatalf("capture = %q, %v", ref, err)
			}
			assertNoManifests(t, data)
		})
	}
}

func TestCaptureRejectsInvalidIdentityAndInterruptedWalk(t *testing.T) {
	for _, tc := range []struct {
		name, rootID, rel string
		interrupt         bool
	}{
		{name: "missing root", rel: "file"}, {name: "missing path", rootID: "root"},
		{name: "parent traversal", rootID: "root", rel: "../file"},
		{name: "absolute", rootID: "root", rel: "/file"},
		{name: "unclean", rootID: "root", rel: "a/../file"},
		{name: "interrupted", rootID: "root", rel: "file", interrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, branch := t.TempDir(), t.TempDir()
			abs := filepath.Join(branch, "file")
			writeFile(t, abs, []byte("body"), 0o600)
			info, err := os.Stat(abs)
			testutil.FailErr(t, "stat", err)
			store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "manifests"))
			ref, err := store.Capture(t.Context(), "job", func(ctx context.Context, visit func(workspacebaseline.CaptureFile) error) error {
				if err := visit(workspacebaseline.CaptureFile{RootID: tc.rootID, ScopeRel: tc.rel, Abs: abs, Info: info}); err != nil {
					return err
				}
				return context.Canceled
			})
			if err == nil || ref != "" {
				t.Fatalf("capture = %q, %v", ref, err)
			}
			if tc.interrupt && !errors.Is(err, context.Canceled) {
				t.Fatalf("interruption = %v", err)
			}
			assertNoManifests(t, data)
		})
	}
}

func assertNoManifests(t *testing.T, data string) {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(data, "manifests", "*.db"))
	testutil.FailErr(t, "list manifests", err)
	if len(entries) != 0 {
		t.Fatalf("failed capture published %v", entries)
	}
}

func TestOverlayCaptureCanRetryWithRecordedTopology(t *testing.T) {
	data, branch := t.TempDir(), t.TempDir()
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "manifests"))
	writeFile(t, filepath.Join(branch, "file"), []byte("base"), 0o600)
	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(singleRoots(), branch))
	testutil.FailErr(t, "capture", err)
	writeFile(t, filepath.Join(branch, "file"), []byte("changed"), 0o600)
	if result, err := store.CaptureOverlay(t.Context(), "job", baseline, nil, branch); err == nil || result.Path != "" {
		t.Fatalf("unknown topology = %+v, %v", result, err)
	}
	assertBody(t, filepath.Join(branch, "file"), "changed")
	result, err := store.CaptureOverlay(t.Context(), "job", baseline, singleRoots(), branch)
	testutil.FailErr(t, "retry capture", err)
	reader, err := workspacebaseline.Open(t.Context(), result.Path, workspacebaseline.ContentStore(result.Path))
	testutil.FailErr(t, "reopen", err)
	defer func() { _ = reader.Close() }()
	if _, exists, err := reader.LookupIdentity(t.Context(), singleRoots(), "other-root", "file"); err != nil || exists {
		t.Fatalf("exact identity miss = %v, %v", exists, err)
	}
	if _, exists, err := reader.LookupIdentity(t.Context(), nil, "root", "file"); err != nil || !exists {
		t.Fatalf("recorded identity = %v, %v", exists, err)
	}
}

func TestOverlayIdentitySurvivesLabelChangesAndRootOrder(t *testing.T) {
	data, branch := t.TempDir(), t.TempDir()
	roots := []projectroot.RootRef{{ID: "a", Label: "shared", Path: t.TempDir()}, {ID: "z", Label: "app", Path: t.TempDir(), IsPrimary: true}}
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "manifests"))
	for _, root := range roots {
		for _, rel := range []string{"same.txt", "@shared/literal:part"} {
			writeFile(t, filepath.Join(branchDir(t, branch, root.ID), rel), []byte(root.ID+" base"), 0o600)
		}
	}
	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture", err)
	for _, root := range roots {
		writeFile(t, filepath.Join(branchDir(t, branch, root.ID), "same.txt"), []byte(root.ID+" changed"), 0o600)
	}
	overlay, err := store.CaptureOverlay(t.Context(), "job", baseline, roots, branch)
	testutil.FailErr(t, "capture overlay", err)
	roots[0].Label = "renamed"
	seen := map[string]bool{}
	err = workspacebaseline.EachOverlayChange(t.Context(), baseline, overlay.Path, roots, func(change workspacebaseline.OverlayChange) error {
		if !change.BaseExists || change.Rel != "same.txt" {
			t.Fatalf("change = %+v", change)
		}
		seen[change.RootID] = true
		return nil
	})
	testutil.FailErr(t, "read renamed overlay", err)
	if len(seen) != 2 {
		t.Fatalf("roots = %v", seen)
	}
	rebuilt := t.TempDir()
	_, err = workspacebaseline.Materialize(t.Context(), baseline, overlay.Path, workspacebaseline.ContentStore(baseline), roots, rebuilt, nil)
	testutil.FailErr(t, "restore", err)
	for _, root := range roots {
		assertBody(t, filepath.Join(branchDir(t, rebuilt, root.ID), "same.txt"), root.ID+" changed")
		assertBody(t, filepath.Join(branchDir(t, rebuilt, root.ID), "@shared/literal:part"), root.ID+" base")
	}
}

func TestReaderRejectsIdentitylessFormatTwoWithoutMutation(t *testing.T) {
	data := t.TempDir()
	manifest := filepath.Join(data, "invalid.db")
	database, err := sql.Open("sqlite", manifest)
	testutil.FailErr(t, "open fixture", err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE manifest(version INTEGER); INSERT INTO manifest VALUES(2);
 CREATE TABLE files(root_id TEXT,scope_rel TEXT,path TEXT,size INTEGER,mtime_ns INTEGER,sha256 TEXT,opaque INTEGER,mode INTEGER,link_target TEXT,deleted INTEGER);
 INSERT INTO files VALUES('','file','file',0,0,'',0,0,'',0);`)
	testutil.FailErr(t, "write fixture", err)
	testutil.FailErr(t, "close fixture", database.Close())
	before, err := os.ReadFile(manifest)
	testutil.FailErr(t, "read before", err)
	if reader, err := workspacebaseline.Open(t.Context(), manifest, sourceblob.New(filepath.Join(data, "source-content"))); err == nil {
		_ = reader.Close()
		t.Fatal("identityless format 2 accepted")
	}
	after, err := os.ReadFile(manifest)
	testutil.FailErr(t, "read after", err)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected manifest changed")
	}
}
