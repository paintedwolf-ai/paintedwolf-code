package board

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func fixtureSnapshot() api.BoardSnapshot {
	return api.BoardSnapshot{
		Repo: api.RepoBrief{
			Languages: []string{"Go", "TypeScript"},
			FileCount: 142,
		},
		Git: &api.BoardGitSlice{Available: true, Branch: "main", Dirty: true, StagedCount: 1, UnstagedCount: 2},
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{{
				ID: "t1", Status: api.WorkerStatusRunning,
				StartedAt: ptrTime(time.Date(2026, 5, 31, 13, 52, 0, 0, time.UTC)),
			}},
		},
		DetailLevel: api.BoardDetailLevelCompact,
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestCompactFormatterInjectBudget(t *testing.T) {
	f := &CompactFormatter{}
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	text, truncated, _ := f.FormatInject(fixtureSnapshot(), false, now)
	if len(text) > api.MaxBoardInjectChars+len(packboard.PackBoardSentinel)+1 {
		t.Fatalf("len=%d want <=%d text=%q", len(text), api.MaxBoardInjectChars, text)
	}
	if !strings.HasPrefix(text, packboard.PackBoardSentinel) {
		t.Fatalf("missing sentinel: %q", text)
	}
	_ = truncated
}

// A snapshot whose orientation lines exceed the inject budget is cut to the
// budget exactly, with the sentinel kept ahead of the body.
func TestInjectFormatterFitsOversizedSnapshotInBudget(t *testing.T) {
	langs := make([]string, 0, 50)
	for range 50 {
		langs = append(langs, strings.Repeat("x", 20))
	}
	snap := api.BoardSnapshot{
		Repo:        api.RepoBrief{Languages: langs, FileCount: 9999},
		DetailLevel: api.BoardDetailLevelCompact,
	}
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	full := strings.Join(packboard.BuildOrientationLines(snap, packboard.OrientOpts{Now: now}), "\n")
	if len(full) <= api.MaxBoardInjectChars {
		t.Fatalf("fixture must exceed the budget before fitting: len=%d", len(full))
	}
	text, ok := DefaultInjectFormatter().FormatBoardInject(snap, false, now)
	if !ok {
		t.Fatal("oversized snapshot produced no inject")
	}
	body, found := strings.CutPrefix(text, packboard.PackBoardSentinel+"\n")
	if !found {
		t.Fatalf("missing sentinel: %q", text)
	}
	if len(body) > api.MaxBoardInjectChars {
		t.Fatalf("body len=%d want <=%d", len(body), api.MaxBoardInjectChars)
	}
}

func TestCompactFormatterOmitsEmptyRepo(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Repo = api.RepoBrief{}
	f := &CompactFormatter{}
	got := f.FormatWithOpts(snap, FormatOpts{MaxChars: 480, Now: time.Now().UTC()})
	if strings.Contains(got, "Repo:") {
		t.Fatalf("got %q", got)
	}
}

func TestCompactFormatterRepoLineFormat(t *testing.T) {
	f := &CompactFormatter{}
	got := f.FormatWithOpts(fixtureSnapshot(), FormatOpts{MaxChars: 480, Now: time.Now().UTC()})
	if !strings.Contains(got, "Repo: 142 files · Go+TypeScript") {
		t.Fatalf("got %q", got)
	}
}

func TestCompactFormatterGitDirty(t *testing.T) {
	f := &CompactFormatter{}
	got := f.FormatWithOpts(fixtureSnapshot(), FormatOpts{MaxChars: 480, Now: time.Now().UTC()})
	if !strings.Contains(got, "Git: main · dirty (3)") {
		t.Fatalf("got %q", got)
	}
}

func TestCompactFormatterWorkRelative(t *testing.T) {
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	f := &CompactFormatter{}
	got := f.FormatWithOpts(fixtureSnapshot(), FormatOpts{MaxChars: 480, Now: now})
	if !strings.Contains(got, "Work: 1 in flight") || !strings.Contains(got, "started 8m ago") {
		t.Fatalf("got %q", got)
	}
}

func TestCompactFormatterFullDetailAddsTasks(t *testing.T) {
	snap := fixtureSnapshot()
	snap.DetailLevel = api.BoardDetailLevelFull
	f := &CompactFormatter{}
	got := f.FormatWithOpts(snap, FormatOpts{MaxChars: api.MaxBoardDetailChars, Now: time.Now().UTC()})
	if !strings.Contains(got, "Task:") {
		t.Fatalf("got %q", got)
	}
}
