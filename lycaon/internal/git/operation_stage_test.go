package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func stageFixture(t *testing.T) string {
	t.Helper()
	return CloneFixtureRepo(t, "stage-fixture", func(dir string) {
		gittest.Init(t, dir)
		gittest.Run(t, dir, "config", "user.name", "Test")
		gittest.Run(t, dir, "config", "user.email", "test@example.com")
		for name, body := range map[string]string{"a.txt": "base\n", "gone.txt": "bye\n", "other.txt": "other\n"} {
			testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
		}
		gittest.Run(t, dir, "add", "--", "a.txt", "gone.txt", "other.txt")
		gittest.Run(t, dir, "commit", "-m", "Initial")
		testutil.FailErr(t, "unrelated staged edit", os.WriteFile(filepath.Join(dir, "other.txt"), []byte("staged elsewhere\n"), 0o644))
		gittest.Run(t, dir, "add", "--", "other.txt")
	})
}

func TestStageWritesExactIndexEntriesAndLeavesTheWorkTree(t *testing.T) {
	t.Parallel()
	dir := stageFixture(t)
	var reviewed []RestoreFile
	result, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stage", Entries: []IndexEntry{
		{Path: "a.txt", Mode: "100755", Content: []byte("index only\n")},
		{Path: "new/b.txt", Mode: "100644", Content: []byte("created\n")},
		{Path: "gone.txt", Remove: true},
	}, Review: func(_ context.Context, files []RestoreFile) error {
		reviewed = files
		return nil
	}})
	testutil.FailErr(t, "stage", err)
	if result.Status != "completed" {
		t.Fatalf("stage = %+v", result)
	}
	if len(reviewed) != 3 || !reviewed[0].IndexOnly {
		t.Fatalf("review = %+v", reviewed)
	}
	if got := gittest.Run(t, dir, "show", ":a.txt"); got != "index only\n" {
		t.Fatalf("index a.txt = %q", got)
	}
	if got := gittest.Run(t, dir, "ls-files", "-s", "a.txt"); !strings.HasPrefix(got, "100755 ") {
		t.Fatalf("index mode = %q", got)
	}
	if got := gittest.Run(t, dir, "show", ":new/b.txt"); got != "created\n" {
		t.Fatalf("index new/b.txt = %q", got)
	}
	if got := gittest.Run(t, dir, "ls-files", "gone.txt"); got != "" {
		t.Fatalf("gone.txt still staged: %q", got)
	}
	if got := gittest.Run(t, dir, "show", ":other.txt"); got != "staged elsewhere\n" {
		t.Fatalf("unrelated staged work changed: %q", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	testutil.FailErr(t, "read work tree", err)
	if string(data) != "base\n" {
		t.Fatalf("work tree changed: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "new", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("stage created a work tree file: %v", err)
	}
}

func TestStageRecordsConflictStages(t *testing.T) {
	t.Parallel()
	dir := stageFixture(t)
	operateStage := func(entries ...IndexEntry) (OperationResult, error) {
		return NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stage", Entries: entries})
	}
	result, err := operateStage(IndexEntry{Path: "a.txt", Mode: "100644", Stages: &[3][]byte{[]byte("base\n"), []byte("ours\n"), []byte("theirs\n")}})
	testutil.FailErr(t, "stage conflict", err)
	if result.Status != "conflicts" || len(result.After.Conflicts) != 1 || result.After.Conflicts[0] != "a.txt" {
		t.Fatalf("conflict = %+v", result)
	}
	for stage, want := range map[string]string{"1": "base\n", "2": "ours\n", "3": "theirs\n"} {
		if got := gittest.Run(t, dir, "show", ":"+stage+":a.txt"); got != want {
			t.Fatalf("stage %s = %q", stage, got)
		}
	}
	if _, err := operateStage(IndexEntry{Path: "a.txt", Mode: "100644", Content: []byte("resolved\n")}); err != nil {
		t.Fatalf("staging a resolution over a conflict: %v", err)
	}
	if got := gittest.Run(t, dir, "ls-files", "-u"); got != "" {
		t.Fatalf("conflict stages remain: %q", got)
	}
}

func TestStageRefusesInvalidRequests(t *testing.T) {
	t.Parallel()
	dir := stageFixture(t)
	for name, entries := range map[string][]IndexEntry{
		"empty":     nil,
		"mode":      {{Path: "a.txt", Mode: "160000", Content: []byte("x")}},
		"duplicate": {{Path: "a.txt", Mode: "100644"}, {Path: "a.txt", Remove: true}},
		"escape":    {{Path: "../out.txt", Mode: "100644"}},
		"absolute":  {{Path: filepath.Join(dir, "a.txt"), Mode: "100644"}},
	} {
		_, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stage", Entries: entries})
		var refusal *OperationError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestMergeFileMarksConflictsWithLabels(t *testing.T) {
	t.Parallel()
	merged, conflicts, err := MergeFile(t.Context(), []byte("a\nours\nc\n"), []byte("a\nb\nc\n"), []byte("a\ntheirs\nc\n"), [3]string{"ours", "base", "theirs"})
	testutil.FailErr(t, "merge conflict", err)
	if conflicts != 1 || string(merged) != "a\n<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\nc\n" {
		t.Fatalf("merged %d: %q", conflicts, merged)
	}
	merged, conflicts, err = MergeFile(t.Context(), []byte("A\nb\nc\n"), []byte("a\nb\nc\n"), []byte("a\nb\nC\n"), [3]string{"ours", "base", "theirs"})
	testutil.FailErr(t, "clean merge", err)
	if conflicts != 0 || string(merged) != "A\nb\nC\n" {
		t.Fatalf("clean merge %d: %q", conflicts, merged)
	}
}

func TestStagePreservesSymbolicLinkIndexMode(t *testing.T) {
	t.Parallel()
	dir := stageFixture(t)
	result, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stage", Entries: []IndexEntry{{Path: "link", Mode: "120000", Content: []byte("a.txt")}}})
	testutil.FailErr(t, "stage symbolic link", err)
	if result.Status != "completed" || !strings.HasPrefix(gittest.Run(t, dir, "ls-files", "--stage", "--", "link"), "120000 ") || gittest.Run(t, dir, "show", ":link") != "a.txt" {
		t.Fatalf("stage symbolic link %+v", result)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); !os.IsNotExist(err) {
		t.Fatal("index operation created a saved link")
	}
}

func TestStageReusesExistingObjectWithMetadataReview(t *testing.T) {
	t.Parallel()
	dir := stageFixture(t)
	oid := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", ":a.txt"))
	var reviewed []RestoreFile
	result, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stage", Entries: []IndexEntry{{Path: "a.txt", Remove: true}, {Path: "renamed", Mode: "100644", ObjectID: oid}}, Review: func(_ context.Context, files []RestoreFile) error { reviewed = files; return nil }})
	testutil.FailErr(t, "reuse existing index object", err)
	if result.Status != "completed" || strings.TrimSpace(gittest.Run(t, dir, "rev-parse", ":renamed")) != oid {
		t.Fatalf("stage %+v", result)
	}
	if len(reviewed) != 2 {
		t.Fatalf("reviewed %d paths", len(reviewed))
	}
	for _, file := range reviewed {
		if len(file.Before.Bytes) != 0 || len(file.After.Bytes) != 0 || file.Before.ObjectID == "" && file.After.ObjectID == "" {
			t.Fatalf("review read blob bytes %+v", file)
		}
	}
}
