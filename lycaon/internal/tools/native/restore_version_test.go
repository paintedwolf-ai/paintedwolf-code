package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
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
