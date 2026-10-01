package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func commitHistoryFile(t *testing.T, dir string, run func(...string) string, path, body string) string {
	t.Helper()
	testutil.FailErr(t, "write history file", os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
	run("--literal-pathspecs", "add", "--", path)
	run("commit", "-m", body)
	return run("rev-parse", "HEAD")
}

func TestFileHistoryCachePinsHeadAndCopiesRows(t *testing.T) {
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	dir, run := fileHistoryRepo(t)
	first := commitHistoryFile(t, dir, run, "a.txt", "one")
	manager := NewManager()
	original, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "first history", err)
	original[0].Subject = "mutated"
	again, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "cached history", err)
	if len(again) != 1 || again[0].Subject != "one" {
		t.Fatalf("cached rows: %+v", again)
	}
	second := commitHistoryFile(t, dir, run, "a.txt", "two")
	changed, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "changed head", err)
	if len(changed) != 2 || changed[0].Hash != second {
		t.Fatalf("stale head: %+v", changed)
	}
	pinned, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt", Revision: first})
	testutil.FailErr(t, "pinned history", err)
	if len(pinned) != 1 || pinned[0].Hash != first {
		t.Fatalf("pinned rows: %+v", pinned)
	}
	hits, reads := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		testutil.FailErr(t, "decode history timing", json.Unmarshal([]byte(line), &record))
		if record["msg"] == "git file history acquisition" {
			reads++
		}
		if record["msg"] == "git file history" && record["cache"] == "hit" {
			hits++
		}
	}
	if hits != 2 || reads != 2 {
		t.Fatalf("hits=%d acquisitions=%d", hits, reads)
	}
}

func TestFileHistoryCacheSeparatesPagesAndBypassesShallowHistory(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	commitHistoryFile(t, dir, run, "a.txt", "one")
	tip := commitHistoryFile(t, dir, run, "a.txt", "two")
	manager := NewManager()
	all, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "complete history", err)
	page, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt", Skip: 1, Limit: 1})
	testutil.FailErr(t, "second page", err)
	if len(all) != 2 || len(page) != 1 || page[0].Hash != all[1].Hash {
		t.Fatalf("pages: %+v %+v", all, page)
	}
	shallow := filepath.Join(dir, ".git", "shallow")
	testutil.FailErr(t, "create shallow boundary", os.WriteFile(shallow, []byte(tip+"\n"), 0o644))
	rows, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "shallow history", err)
	if len(rows) != 1 {
		t.Fatalf("reused complete history in shallow repository: %+v", rows)
	}
	testutil.FailErr(t, "remove shallow boundary", os.Remove(shallow))
	rows, err = manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "deepened history", err)
	if len(rows) != 2 {
		t.Fatalf("stale shallow history: %+v", rows)
	}
}

func TestFileHistoryLiteralPathsAndEmptyPages(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	manager := NewManager()
	for _, path := range []string{"a*.txt", "a1.txt", "[abc].txt", "tab\tnewline\n雪.txt", " leading and trailing "} {
		tip := commitHistoryFile(t, dir, run, path, "created")
		rows, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: path})
		testutil.FailErr(t, "literal path history", err)
		if len(rows) != 1 || rows[0].Path != path || rows[0].Hash != tip {
			t.Fatalf("path %q: %+v", path, rows)
		}
	}
	for range 2 {
		rows, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "never-existed"})
		testutil.FailErr(t, "empty history", err)
		if len(rows) != 0 {
			t.Fatalf("empty history: %+v", rows)
		}
	}
	if manager.histories.order.Len() != 6 {
		t.Fatalf("empty result not retained: %d entries", manager.histories.order.Len())
	}
}

func TestFileHistoryRetriesUnbornRepository(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	manager := NewManager()
	_, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	if err == nil || manager.histories.order.Len() != 0 {
		t.Fatal("unborn repository cached as success")
	}
	commitHistoryFile(t, dir, run, "a.txt", "one")
	rows, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "retry after first commit", err)
	if len(rows) != 1 {
		t.Fatalf("retry rows: %+v", rows)
	}
}

func TestFileHistoryCacheCanceledHitAndRepositoryIsolation(t *testing.T) {
	manager := NewManager()
	for _, subject := range []string{"first repository", "second repository"} {
		dir, run := fileHistoryRepo(t)
		commitHistoryFile(t, dir, run, "a.txt", subject)
		rows, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
		testutil.FailErr(t, "repository history", err)
		if len(rows) != 1 || rows[0].Subject != subject {
			t.Fatalf("crossed repository cache: %+v", rows)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = manager.FileHistory(ctx, dir, GitFileHistoryOpts{Path: "a.txt"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled cache hit: %v", err)
		}
	}
}

func TestFileHistoryDoesNotFollowReplacementObjects(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	first := commitHistoryFile(t, dir, run, "a.txt", "one")
	tip := commitHistoryFile(t, dir, run, "a.txt", "two")
	manager := NewManager()
	run("replace", tip, first)
	rows, err := manager.FileHistory(t.Context(), dir, GitFileHistoryOpts{Path: "a.txt"})
	testutil.FailErr(t, "original commit lineage", err)
	if len(rows) != 2 || rows[0].Subject != "two" {
		t.Fatalf("replacement redirected history: %+v", rows)
	}
}

func TestParseFileHistoryPreservesObjectFormatsAndControlCharacters(t *testing.T) {
	for _, width := range []int{40, 64} {
		hash, oid := strings.Repeat("a", width), strings.Repeat("b", width)
		subject, path := "subject\x1e\x1f", " file\tname\n "
		raw := "\x00" + hash + "\x00Author\x001700000000\x001700000001\x00" + subject + "\x00\x00\n:100644 100644 " + oid + " " + oid + " M\x00" + path + "\x00"
		rows, err := parseFileHistory(raw)
		testutil.FailErr(t, "parse full object history", err)
		if len(rows) != 1 || rows[0].Hash != hash || rows[0].BlobOID != oid || rows[0].Path != path || rows[0].Subject != subject {
			t.Fatalf("width %d: %+v", width, rows)
		}
		if _, err := parseFileHistory(strings.Replace(raw, ":100644", ":broken\x00", 1)); err == nil {
			t.Fatal("malformed raw record accepted")
		}
	}
}
