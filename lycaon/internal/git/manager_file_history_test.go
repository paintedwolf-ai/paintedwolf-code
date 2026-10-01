package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fileHistoryRepo(t *testing.T) (dir string, run func(args ...string) string) {
	t.Helper()
	dir = t.TempDir()
	initGitRepo(t, dir)
	run = func(args ...string) string {
		t.Helper()
		cmd := osexec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = exec.LocalGitEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	return dir, run
}

func TestFileHistoryUsesCurrentPathWithBlobIdentity(t *testing.T) {
	ctx := context.Background()
	dir, run := fileHistoryRepo(t)
	write := func(rel, content string) {
		t.Helper()
		testutil.FailErr(t, "write "+rel,
			os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
	}
	write("old.txt", "one\n")
	run("add", "-A")
	run("commit", "-m", "create old")
	write("old.txt", "one\ntwo\n")
	run("add", "-A")
	run("commit", "-m", "grow old")
	run("mv", "old.txt", "new.txt")
	run("commit", "-m", "rename to new")
	write("new.txt", "one\ntwo\nthree\n")
	run("add", "-A")
	run("commit", "-m", "grow new")

	mgr := NewManager()
	commits, err := mgr.FileHistory(ctx, dir, GitFileHistoryOpts{Path: "new.txt"})
	testutil.FailErr(t, "file history", err)
	if len(commits) != 2 {
		t.Fatalf("commits = %+v", commits)
	}
	subjects := make([]string, 0, len(commits))
	for _, c := range commits {
		subjects = append(subjects, c.Subject)
	}
	if strings.Join(subjects, "|") != "grow new|rename to new" {
		t.Fatalf("subjects = %v", subjects)
	}
	// Current-path history stops at the rename boundary.
	if commits[0].Path != "new.txt" || commits[1].Path != "new.txt" {
		t.Fatalf("paths = %q %q", commits[0].Path, commits[1].Path)
	}
	wantTip := run("rev-parse", "HEAD:new.txt")
	if commits[0].BlobOID != wantTip {
		t.Fatalf("tip blob = %q want %q", commits[0].BlobOID, wantTip)
	}
	for _, c := range commits {
		if c.Hash == "" || c.AuthorName != "Test" || c.BlobOID == "" ||
			c.AuthoredAt.IsZero() || c.CommittedAt.IsZero() {
			t.Fatalf("commit = %+v", c)
		}
	}

	// Positional paging continues the same lineage.
	page, err := mgr.FileHistory(ctx, dir, GitFileHistoryOpts{Path: "new.txt", Skip: 1, Limit: 1})
	testutil.FailErr(t, "file history page", err)
	if len(page) != 1 || page[0].Subject != "rename to new" {
		t.Fatalf("page = %+v", page)
	}
}

func TestFileHistoryReportsARemovalWithoutABlob(t *testing.T) {
	ctx := context.Background()
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "write doomed",
		os.WriteFile(filepath.Join(dir, "doomed.txt"), []byte("bytes\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "create doomed")
	run("rm", "doomed.txt")
	run("commit", "-m", "remove doomed")

	commits, err := NewManager().FileHistory(ctx, dir, GitFileHistoryOpts{Path: "doomed.txt"})
	testutil.FailErr(t, "file history", err)
	if len(commits) != 2 {
		t.Fatalf("commits = %+v", commits)
	}
	if commits[0].Subject != "remove doomed" || commits[0].BlobOID != "" {
		t.Fatalf("removal = %+v", commits[0])
	}
	if commits[1].BlobOID == "" {
		t.Fatalf("creation lost its blob: %+v", commits[1])
	}
}

// Timeout classification stays distinct from other failures.
func TestFileHistoryFailureSeparatesBudgetFromEveryOtherOutcome(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(
		context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	live := context.Background()
	boom := errors.New("git exploded")

	cases := []struct {
		name   string
		parent context.Context
		walk   context.Context
		err    error
		code   int
		want   error
	}{
		{"clean run", live, live, nil, 0, nil},
		{"nonzero exit is read from output, not here", live, live, boom, 128, nil},
		{"our budget elapsed", live, expired, boom, -1, ErrFileHistoryTimeout},
		{"subprocess reported its own deadline", live, live,
			fmt.Errorf("%w after 5s", exec.ErrTimeout), -1, ErrFileHistoryTimeout},
		{"caller went away", canceled, canceled, boom, -1, context.Canceled},
		{"genuine failure", live, live, boom, -1, boom},
		{"truncated successful process", live, live, exec.ErrOutputTruncated, 0, exec.ErrOutputTruncated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fileHistoryFailure(tc.parent, tc.walk, tc.err, tc.code)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAncestorAnswersLineageAndReportsUnknownObjects(t *testing.T) {
	ctx := context.Background()
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "write a",
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "first")
	first := run("rev-parse", "HEAD")
	testutil.FailErr(t, "write b",
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "second")
	second := run("rev-parse", "HEAD")

	mgr := NewManager()
	forward, err := mgr.IsAncestor(ctx, dir, first, second)
	testutil.FailErr(t, "ancestor forward", err)
	backward, err := mgr.IsAncestor(ctx, dir, second, first)
	testutil.FailErr(t, "ancestor backward", err)
	unknown, err := mgr.IsAncestor(ctx, dir, strings.Repeat("f", 40), second)
	if err == nil {
		t.Fatal("unknown ancestor should report that Git could not answer")
	}
	if !forward || backward || unknown {
		t.Fatalf("forward=%v backward=%v unknown=%v", forward, backward, unknown)
	}
}

func TestBlobContentHonorsLimitsAboveDefaultCommandOutput(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	content := strings.Repeat("line\n", 240000)
	testutil.FailErr(t, "write large text blob", os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0o644))
	oid := run("hash-object", "-w", "large.txt")
	mgr := NewManager()
	raw, ok, err := mgr.BlobContent(t.Context(), dir, oid, len(content))
	testutil.FailErr(t, "read complete large blob", err)
	if !ok || string(raw) != content {
		t.Fatalf("large blob truncated: bytes=%d ok=%v", len(raw), ok)
	}
	_, ok, err = mgr.BlobContent(t.Context(), dir, oid, len(content)-1)
	testutil.FailErr(t, "enforce blob limit", err)
	if ok {
		t.Fatal("over-limit blob reported complete")
	}
}

func TestBlobContentRoundTripsAndRefusesOverLongObjects(t *testing.T) {
	ctx := context.Background()
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "write blob",
		os.WriteFile(filepath.Join(dir, "blob.txt"), []byte("exact bytes\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "blob")
	oid := run("rev-parse", "HEAD:blob.txt")

	mgr := NewManager()
	raw, ok, err := mgr.BlobContent(ctx, dir, oid, 64)
	testutil.FailErr(t, "blob content", err)
	if !ok || string(raw) != "exact bytes\n" {
		t.Fatalf("blob = %q ok=%v", raw, ok)
	}
	_, ok, err = mgr.BlobContent(ctx, dir, oid, 4)
	testutil.FailErr(t, "blob content capped", err)
	if ok {
		t.Fatal("over-long object reported ok")
	}
	_, ok, err = mgr.BlobContent(ctx, dir, strings.Repeat("f", 40), 64)
	testutil.FailErr(t, "blob content unknown", err)
	if ok {
		t.Fatal("unknown object reported ok")
	}
}
