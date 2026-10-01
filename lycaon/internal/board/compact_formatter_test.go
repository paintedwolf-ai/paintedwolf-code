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
		Delegation: &api.BoardDelegationSlice{
			"delegations": []api.Delegation{{
				ID: "delegation-abc12345", Phase: api.DelegationPhaseWorker,
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

func TestCompactFormatterOmitsDelegationWhenRequested(t *testing.T) {
	f := &CompactFormatter{}
	got := f.FormatWithOpts(fixtureSnapshot(), FormatOpts{MaxChars: 480, Now: time.Now().UTC(), OmitDelegation: true})
	if strings.Contains(got, "Delegation:") {
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
