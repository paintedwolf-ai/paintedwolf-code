package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommitReviewPreservesMissingShallowParent(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	path := filepath.Join(dir, "file.txt")
	testutil.FailErr(t, "write original", os.WriteFile(path, []byte("original\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "original")
	parent := run("rev-parse", "HEAD")
	testutil.FailErr(t, "write change", os.WriteFile(path, []byte("changed\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "changed")
	commit := run("rev-parse", "HEAD")
	clone := filepath.Join(t.TempDir(), "shallow")
	run("clone", "--depth=1", "file://"+dir, clone)
	mgr := NewManager()
	metadata, err := mgr.CommitDetails(t.Context(), clone, commit)
	testutil.FailErr(t, "read shallow metadata", err)
	if len(metadata.Parents) != 1 || metadata.Parents[0] != parent {
		t.Fatalf("lost shallow parent: %+v", metadata)
	}
	_, err = mgr.CommitReview(t.Context(), clone, CommitReviewOptions{Before: parent, After: commit, Limit: 100})
	if err == nil {
		t.Fatal("missing shallow parent appeared as a successful comparison")
	}
}

func TestCommitReviewIgnoresReplacementObjects(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	path := filepath.Join(dir, "file.txt")
	testutil.FailErr(t, "write original", os.WriteFile(path, []byte("original\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "original commit")
	commit, blob := run("rev-parse", "HEAD"), run("rev-parse", "HEAD:file.txt")
	testutil.FailErr(t, "write replacement", os.WriteFile(path, []byte("replacement contents\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "replacement commit")
	replacement, replacementBlob := run("rev-parse", "HEAD"), run("rev-parse", "HEAD:file.txt")
	run("replace", commit, replacement)
	run("replace", blob, replacementBlob)
	mgr := NewManager()
	metadata, err := mgr.CommitDetails(t.Context(), dir, commit)
	testutil.FailErr(t, "read original metadata", err)
	if metadata.Message != "original commit" || len(metadata.Parents) != 0 {
		t.Fatalf("replacement metadata: %+v", metadata)
	}
	page, err := mgr.CommitReview(t.Context(), dir, CommitReviewOptions{After: commit, Limit: 100})
	testutil.FailErr(t, "read original tree", err)
	if len(page.Files) != 1 || page.Files[0].AfterOID != blob {
		t.Fatalf("replacement tree: %+v", page)
	}
	size, err := mgr.BlobSize(t.Context(), dir, blob)
	testutil.FailErr(t, "read original size", err)
	content, ok, err := mgr.BlobContent(t.Context(), dir, blob, 100)
	testutil.FailErr(t, "read original content", err)
	if size != 9 || !ok || string(content) != "original\n" {
		t.Fatalf("replacement blob: size=%d ok=%v content=%q", size, ok, content)
	}
}

func TestCommitReviewImmutablePagedFiles(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	write := func(path string, content []byte) {
		t.Helper()
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, path), content, 0o644))
	}
	write("old.txt", []byte("same\n"))
	write("gone.txt", []byte("gone\n"))
	write("mode.sh", []byte("echo test\n"))
	run("add", "-A")
	run("commit", "-m", "initial")
	before := run("rev-parse", "HEAD")
	run("mv", "old.txt", "new\tname.txt")
	run("rm", "gone.txt")
	write("binary", []byte{0, 1, 2})
	write("added\nfile.txt", []byte("first\nsecond\n"))
	testutil.FailErr(t, "set executable", os.Chmod(filepath.Join(dir, "mode.sh"), 0o755))
	run("add", "-A")
	run("commit", "-m", "record changes", "-m", "A full message body.")
	after := run("rev-parse", "HEAD")
	write("added\nfile.txt", []byte("later working contents\n"))
	mgr := NewManager()
	metadata, err := mgr.CommitDetails(t.Context(), dir, after)
	testutil.FailErr(t, "commit metadata", err)
	if metadata.Hash != after || metadata.AuthorName != "Test" || strings.Join(metadata.Parents, "") != before || !strings.Contains(metadata.Message, "A full message body.") {
		t.Fatalf("metadata = %+v", metadata)
	}
	files := map[string]CommitReviewFile{}
	offset := 0
	for {
		page, err := mgr.CommitReview(t.Context(), dir, CommitReviewOptions{Before: before, After: after, Offset: offset, Limit: 2})
		testutil.FailErr(t, "review page", err)
		if page.Total != 5 || page.Insertions != 2 || page.Deletions != 1 || len(page.Files) > 2 {
			t.Fatalf("page = %+v", page)
		}
		for _, row := range page.Files {
			if _, duplicate := files[row.Path]; duplicate {
				t.Fatalf("duplicate path %q", row.Path)
			}
			files[row.Path] = row
		}
		if page.NextOffset == 0 {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("cursor did not advance")
		}
		offset = page.NextOffset
	}
	if len(files) != 5 || files["new\tname.txt"].BeforePath != "old.txt" || files["new\tname.txt"].Status != "R" || !files["binary"].Binary || files["mode.sh"].AfterMode != "100755" || files["gone.txt"].AfterOID != "" {
		t.Fatalf("files = %+v", files)
	}
	selected, err := mgr.CommitReview(t.Context(), dir, CommitReviewOptions{Before: before, After: after, Path: "new\tname.txt", Limit: 1})
	testutil.FailErr(t, "select renamed path", err)
	if len(selected.Files) != 1 || selected.Files[0].BeforePath != "old.txt" {
		t.Fatalf("selection = %+v", selected)
	}
}

func TestCommitReviewInitialEmptyAndNestedRoots(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "create nested root", os.Mkdir(filepath.Join(dir, "nested"), 0o755))
	testutil.FailErr(t, "write nested", os.WriteFile(filepath.Join(dir, "nested", "one.txt"), []byte("one\n"), 0o644))
	testutil.FailErr(t, "write outside", os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("outside\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "initial")
	initial := run("rev-parse", "HEAD")
	mgr := NewManager()
	page, err := mgr.CommitReview(t.Context(), filepath.Join(dir, "nested"), CommitReviewOptions{After: initial, Limit: 100})
	testutil.FailErr(t, "initial nested review", err)
	if page.Total != 1 || len(page.Files) != 1 || page.Files[0].Path != "one.txt" || page.Insertions != 1 {
		t.Fatalf("nested page = %+v", page)
	}
	run("commit", "--allow-empty", "-m", "empty")
	after := run("rev-parse", "HEAD")
	page, err = mgr.CommitReview(t.Context(), dir, CommitReviewOptions{Before: initial, After: after, Limit: 100})
	testutil.FailErr(t, "empty review", err)
	if page.Total != 0 || len(page.Files) != 0 {
		t.Fatalf("empty page = %+v", page)
	}
	_, err = mgr.CommitReview(t.Context(), dir, CommitReviewOptions{Before: strings.Repeat("f", 40), After: after, Limit: 100})
	if err == nil {
		t.Fatal("missing before object appeared empty")
	}
	_, err = mgr.CommitDetails(t.Context(), dir, "--help")
	if err == nil {
		t.Fatal("accepted option as object id")
	}
}

func TestCommitReviewCrossRootRenameDoesNotExposeSibling(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "create nested root", os.Mkdir(filepath.Join(dir, "nested"), 0o755))
	testutil.FailErr(t, "write outside", os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("same\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "initial")
	before := run("rev-parse", "HEAD")
	run("mv", "outside.txt", "nested/inside.txt")
	run("commit", "-m", "move in")
	page, err := NewManager().CommitReview(t.Context(), filepath.Join(dir, "nested"), CommitReviewOptions{Before: before, After: run("rev-parse", "HEAD"), Limit: 100})
	testutil.FailErr(t, "nested rename review", err)
	if page.Total != 1 || page.Files[0].Path != "inside.txt" || page.Files[0].BeforeOID != "" || page.Files[0].BeforePath != "inside.txt" {
		t.Fatalf("cross-root path exposed: %+v", page)
	}
}

func TestCommitReviewUsesCommittedAttributes(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "write attributes", os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt diff\n"), 0o644))
	testutil.FailErr(t, "write before", os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "before")
	before := run("rev-parse", "HEAD")
	testutil.FailErr(t, "write after", os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\ntwo\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "after")
	after := run("rev-parse", "HEAD")
	testutil.FailErr(t, "change working attributes", os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt -diff\n"), 0o644))
	page, err := NewManager().CommitReview(t.Context(), dir, CommitReviewOptions{Before: before, After: after, Limit: 100})
	testutil.FailErr(t, "review with working attributes", err)
	if page.Total != 1 || page.Insertions != 1 || page.Files[0].Binary {
		t.Fatalf("working attributes changed the review: %+v", page)
	}
}
