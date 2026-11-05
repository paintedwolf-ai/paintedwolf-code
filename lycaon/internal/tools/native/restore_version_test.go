package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRestoreVersionWithExplicitVersionID(t *testing.T) {
	dir := t.TempDir()
	v1Content := "package main\n\nfunc main() {}\n"
	writeProvenanceFile(t, dir, "main.go", "package main\n\nfunc broken() {\n")

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v2", State: "content"},
		},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f1", ProjectID: "p1", RootID: "r1", Path: "main.go", State: "content", Content: []byte(v1Content)},
			"v2": {ID: "v2", FileID: "f1", ProjectID: "p1", RootID: "r1", Path: "main.go", State: "content", Content: []byte("package main\n\nfunc broken() {\n")},
		},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"version_id": "v1",
	}, tctx)
	testutil.FailErr(t, "restore_version explicit", err)

	if !strings.Contains(out, "Restored main.go to version v1") {
		t.Fatalf("unexpected receipt: %s", out)
	}

	diskBytes, err := os.ReadFile(filepath.Join(dir, "main.go"))
	testutil.FailErr(t, "read restored file", err)
	if string(diskBytes) != v1Content {
		t.Fatalf("disk content = %q, want %q", string(diskBytes), v1Content)
	}
}

func TestRestoreVersionDefaultsToPredecessor(t *testing.T) {
	dir := t.TempDir()
	v1Content := "hello world\n"
	writeProvenanceFile(t, dir, "hello.txt", "hello mutated\n")

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/hello.txt": {FileID: "f_hello", VersionID: "v2", State: "content"},
		},
		fileVersions: map[string][]sourceledger.Version{
			"f_hello": {
				{ID: "v2", FileID: "f_hello"},
				{ID: "v1", FileID: "f_hello"},
			},
		},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f_hello", ProjectID: "p1", RootID: "r1", Path: "hello.txt", State: "content", Content: []byte(v1Content)},
			"v2": {ID: "v2", FileID: "f_hello", ProjectID: "p1", RootID: "r1", Path: "hello.txt", State: "content", Content: []byte("hello mutated\n")},
		},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	out, err := tool.Run(context.Background(), map[string]any{
		"path": "hello.txt",
	}, tctx)
	testutil.FailErr(t, "restore_version predecessor", err)

	if !strings.Contains(out, "Restored hello.txt to version v1") {
		t.Fatalf("unexpected receipt: %s", out)
	}

	diskBytes, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	testutil.FailErr(t, "read restored file", err)
	if string(diskBytes) != v1Content {
		t.Fatalf("disk content = %q, want %q", string(diskBytes), v1Content)
	}
}

func TestRestoreVersionBaseMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v2", State: "content"},
		},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f1", ProjectID: "p1", RootID: "r1", Path: "main.go", State: "content", Content: []byte("v1")},
		},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	_, err := tool.Run(context.Background(), map[string]any{
		"path":            "main.go",
		"version_id":      "v1",
		"base_version_id": "v1", // mismatch: head is v2
	}, tctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SOURCE_VERSION_BASE_MISMATCH" {
		t.Fatalf("err = %v, want SOURCE_VERSION_BASE_MISMATCH reject", err)
	}
}

func TestRestoreVersionNotFoundRejected(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v1", State: "content"},
		},
		versions: map[string]sourceledger.RestorableVersion{},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	_, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"version_id": "v_missing",
	}, tctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SOURCE_VERSION_NOT_FOUND" {
		t.Fatalf("err = %v, want SOURCE_VERSION_NOT_FOUND reject", err)
	}
}

func TestRestoreVersionNoPredecessorRejected(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v1", State: "content"},
		},
		fileVersions: map[string][]sourceledger.Version{
			"f1": {
				{ID: "v1", FileID: "f1"},
			},
		},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	_, err := tool.Run(context.Background(), map[string]any{
		"path": "main.go",
	}, tctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SOURCE_VERSION_NO_PREDECESSOR" {
		t.Fatalf("err = %v, want SOURCE_VERSION_NO_PREDECESSOR reject", err)
	}
}

func TestRestoreVersionRestoresAbsentState(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "newfile.txt", "created in turn\n")

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/newfile.txt": {FileID: "f_new", VersionID: "v2", State: "content"},
		},
		fileVersions: map[string][]sourceledger.Version{
			"f_new": {
				{ID: "v2", FileID: "f_new"},
				{ID: "v1", FileID: "f_new"},
			},
		},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f_new", ProjectID: "p1", RootID: "r1", Path: "newfile.txt", State: "absent"},
			"v2": {ID: "v2", FileID: "f_new", ProjectID: "p1", RootID: "r1", Path: "newfile.txt", State: "content", Content: []byte("created in turn\n")},
		},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	out, err := tool.Run(context.Background(), map[string]any{
		"path": "newfile.txt",
	}, tctx)
	testutil.FailErr(t, "restore_version absent state", err)

	if !strings.Contains(out, "Restored newfile.txt to absent state") {
		t.Fatalf("unexpected receipt: %s", out)
	}

	// Verify file is removed from disk
	if _, statErr := os.Stat(filepath.Join(dir, "newfile.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected newfile.txt to be removed, stat err = %v", statErr)
	}
}

func TestRestoreVersionRecreatesDeletedFile(t *testing.T) {
	dir := t.TempDir()
	v1Content := "this file was deleted earlier\n"
	// deleted.txt is intentionally absent from disk to simulate prior deletion.

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/deleted.txt": {FileID: "f_del", VersionID: "v2", State: "absent"},
		},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f_del", ProjectID: "p1", RootID: "r1", Path: "deleted.txt", State: "content", Content: []byte(v1Content)},
			"v2": {ID: "v2", FileID: "f_del", ProjectID: "p1", RootID: "r1", Path: "deleted.txt", State: "absent"},
		},
	}

	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)

	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "deleted.txt",
		"version_id": "v1",
	}, tctx)
	testutil.FailErr(t, "restore_version recreate deleted", err)

	if !strings.Contains(out, "Restored deleted.txt to version v1") {
		t.Fatalf("unexpected receipt: %s", out)
	}

	diskBytes, err := os.ReadFile(filepath.Join(dir, "deleted.txt"))
	testutil.FailErr(t, "read recreated file", err)
	if string(diskBytes) != v1Content {
		t.Fatalf("disk content = %q, want %q", string(diskBytes), v1Content)
	}
}

// A deleted file has no live head; its history is found through the deletion.
func TestRestoreVersionDefaultsToTheContentBeforeADeletion(t *testing.T) {
	dir := t.TempDir()
	ledger := &fakeSourceLedger{
		deleted:      map[string]sourceledger.DeletedPath{"r1/gone.txt": {FileID: "f_gone", VersionID: "v2"}},
		fileVersions: map[string][]sourceledger.Version{"f_gone": {{ID: "v2"}, {ID: "v1"}}},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f_gone", ProjectID: "p1", RootID: "r1", Path: "gone.txt", State: "content", Content: []byte("kept\n")},
			"v2": {ID: "v2", FileID: "f_gone", ProjectID: "p1", RootID: "r1", Path: "gone.txt", State: "absent"},
		},
	}
	_, err := (&RestoreVersionTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"path": "gone.txt"}, provenanceCtx(dir, ledger))
	testutil.FailErr(t, "restore deleted file", err)
	data, err := os.ReadFile(filepath.Join(dir, "gone.txt"))
	testutil.FailErr(t, "read restored file", err)
	if string(data) != "kept\n" {
		t.Fatalf("restored %q", data)
	}
}

// Restoring a deleted path mints a new file identity; the path's baseline survives it.
func TestRestoreVersionOfADeletedFileIsAnEmptySessionDiffACDF12(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Identity.UserTurn = "p1", "s1", 1
	tctx.Source.SourceLedger = st
	tctx.Source.History = tools.SourceHistory{Files: st.History, Comparison: st.Comparisons, Git: st.Git, Authorship: st.Walk}
	tctx.Source.Commands, tctx.Source.Observations, tctx.Source.GitMutations = st.Commands, st.Inventory, st.Git
	const body = "package main\n"
	writeProvenanceFile(t, dir, "a.go", body)
	testutil.FailErr(t, "record baseline", st.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: "r1", Path: "a.go"}, ProjectID: "p1",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, SessionID: "s0", Turn: 1,
		OperationID: "create", After: []byte(body),
	}))
	baseline, err := st.History.ResolveHead(t.Context(), "p1", sourcebranch.Trunk, "r1", "a.go")
	testutil.FailErr(t, "resolve baseline", err)
	_, err = st.Checkpoints.CreateStructuralCheckpoint(t.Context(), sourceledger.StructuralCheckpointInput{
		ProjectID: "p1", Kind: sourceledger.CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: 1,
	})
	testutil.FailErr(t, "create turn checkpoint", err)
	testutil.FailErr(t, "delete file", os.Remove(filepath.Join(dir, "a.go")))
	testutil.FailErr(t, "record deletion", st.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: "r1", Path: "a.go"}, ProjectID: "p1",
		Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginAgent, SessionID: "s1", Turn: 1, OperationID: "delete",
	}))

	_, err = (&RestoreVersionTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"path": "a.go"}, tctx)
	testutil.FailErr(t, "restore_version", err)
	raw, err := (&survey.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"path": "a.go", "mode": "diff"}, tctx)
	testutil.FailErr(t, "source_history diff", err)
	var diff struct {
		BaseVersionID string `json:"base_version_id"`
		VersionID     string `json:"version_id"`
		Diff          string `json:"diff"`
	}
	testutil.FailErr(t, "decode diff", json.Unmarshal([]byte(raw), &diff))
	if diff.BaseVersionID != baseline.VersionID || diff.VersionID == "" || diff.Diff != "" {
		t.Fatalf("diff = %+v, want an empty diff from %s", diff, baseline.VersionID)
	}
}
