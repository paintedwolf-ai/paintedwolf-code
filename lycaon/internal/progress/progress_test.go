package progress_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
)

var progressKeySeq atomic.Uint64

// uniqueProgressKey returns a process-unique key so tests asserting absolute
// revision/seq values stay deterministic across repeated runs (go test -count),
// which the package's global revision/completion state would otherwise leak.
func uniqueProgressKey(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, progressKeySeq.Add(1))
}

func TestDeriveProgress_parsesChecklist(t *testing.T) {
	content := "## Progress\n- [ ] first\n- [x] second\nnot a step\n* [X] third\n"
	res := progress.DeriveProgress(content, progress.DefaultProgressCap)
	if len(res.Items) != 3 {
		t.Fatalf("items = %d want 3 (%+v)", len(res.Items), res.Items)
	}
	if res.Items[0].State != progress.ProgressStatePending || res.Items[0].Label != "first" {
		t.Fatalf("item0 = %+v", res.Items[0])
	}
	if res.Items[1].State != progress.ProgressStateDone || res.Items[2].State != progress.ProgressStateDone {
		t.Fatalf("done states = %+v", res.Items)
	}
}

func TestProgressMissing(t *testing.T) {
	if !progress.ProgressMissing("# Goal\n\njust prose, no checklist") {
		t.Fatal("expected plan missing when no checklist")
	}
	if progress.ProgressMissing("- [ ] do the thing") {
		t.Fatal("expected plan present with a checklist item")
	}
}

func TestDeriveProgress_clampsOverlongLabel(t *testing.T) {
	long := strings.Repeat("b", progress.MaxLabelRunes+3)
	res := progress.DeriveProgress("- [ ] "+long, 0)
	if len(res.Items) != 1 {
		t.Fatalf("items = %d want 1", len(res.Items))
	}
	if n := len([]rune(res.Items[0].Label)); n > progress.MaxLabelRunes+1 {
		t.Fatalf("label not clamped: %d runes", n)
	}
}

func TestDeriveProgress_supportsFortyEightRows(t *testing.T) {
	content := strings.Repeat("- [ ] step\n", progress.MaxProgressCap)
	res := progress.DeriveProgress(content, progress.MaxProgressCap)
	if len(res.Items) != 48 || res.More != 0 {
		t.Fatalf("DeriveProgress = %d items + %d more, want 48 + 0", len(res.Items), res.More)
	}

	res = progress.DeriveProgress(content+"- [ ] overflow\n", progress.MaxProgressCap+1)
	if len(res.Items) != 48 || res.More != 1 {
		t.Fatalf("capped DeriveProgress = %d items + %d more, want 48 + 1", len(res.Items), res.More)
	}
}

func TestSummarizeUpdate_countsCurrentStates(t *testing.T) {
	steps := progress.DeriveProgress("- [ ] a\n- [x] b\n- [~] c\n", 0).Items
	got := progress.SummarizeUpdate(steps, 17)
	if got.ChangeCount != 17 || got.TotalSteps != 3 || got.Pending != 1 || got.Done != 1 || got.NA != 1 {
		t.Fatalf("SummarizeUpdate = %+v", got)
	}
}

func TestDeriveProgress_parsesNA(t *testing.T) {
	res := progress.DeriveProgress("- [ ] a\n- [x] b\n- [~] c\n- [-] d", progress.DefaultProgressCap)
	if len(res.Items) != 4 {
		t.Fatalf("items = %d want 4 (%+v)", len(res.Items), res.Items)
	}
	if res.Items[2].State != progress.ProgressStateNA || res.Items[3].State != progress.ProgressStateNA {
		t.Fatalf("expected `[~]` and `[-]` → na, got %+v", res.Items)
	}
}

func TestAllTerminal_naCountsTerminal(t *testing.T) {
	if !progress.AllTerminal("- [x] a\n- [~] b") {
		t.Fatal("a plan of done + n/a should be terminal (run complete)")
	}
	if progress.AllTerminal("- [x] a\n- [ ] b") {
		t.Fatal("a pending step should block completion")
	}
}

func TestHasOpenSteps(t *testing.T) {
	if progress.HasOpenSteps("") {
		t.Fatal("missing plan must not count as open steps")
	}
	if progress.HasOpenSteps("- [x] a\n- [x] b") {
		t.Fatal("all-terminal plan must not count as open steps")
	}
	if !progress.HasOpenSteps("- [x] a\n- [ ] b") {
		t.Fatal("pending step must count as open")
	}
	if progress.HasOpenSteps("- [x] a\n- [>] optional closeout") {
		t.Fatal("optional-only remainder must not count as open")
	}
}

func TestOpenStepCount(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"missing plan", "", 0},
		{"all terminal", "- [x] a\n- [~] b", 0},
		{"one open row", "- [x] a\n- [ ] b", 1},
		{"open rows are the dispatch width", "- [ ] a\n- [ ] b\n- [ ] c", 3},
		{"optional rows are not open work", "- [ ] a\n- [>] closeout", 1},
		{"done and n/a both close a row", "- [x] a\n- [~] b\n- [ ] c\n- [ ] d", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := progress.OpenStepCount(tc.content); got != tc.want {
				t.Fatalf("OpenStepCount(%q) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}

func TestDeriveProgress_inlinePlanHeader(t *testing.T) {
	content := "## Progress - [ ] History expansion in state - [ ] Custom prompt format strings - [ ] Command completion engine - [ ] Verify workers and run tests - [>] Synthesize results"
	res := progress.DeriveProgress(content, progress.DefaultProgressCap)
	if len(res.Items) != 4 {
		t.Fatalf("inline plan items = %d want 4 (%+v)", len(res.Items), res.Items)
	}
	if res.Items[0].State != progress.ProgressStatePending || res.Items[0].Label != "History expansion in state" {
		t.Fatalf("item0 = %+v", res.Items[0])
	}
	if res.Items[3].State != progress.ProgressStatePending || res.Items[3].Label != "Verify workers and run tests" {
		t.Fatalf("item3 = %+v", res.Items[3])
	}
}

func TestOptionalState(t *testing.T) {
	// Optional `- [>]` lines are coordinator-only: hidden from the strip digest…
	res := progress.DeriveProgress("- [x] a\n- [>] synthesize report", progress.DefaultProgressCap)
	if len(res.Items) != 1 || res.Items[0].Label != "a" {
		t.Fatalf("optional line must be dropped from the digest, got %+v", res.Items)
	}
	// …but still recognized as non-blocking, so the run is complete with one open.
	if !progress.AllTerminal("- [x] a\n- [>] synthesize report") {
		t.Fatal("an open optional line must not block completion")
	}
	// Optional lines are counted as neither done nor pending.
	if done, pending, na := progress.CloseCounts("- [x] a\n- [>] b"); done != 1 || pending != 0 || na != 0 {
		t.Fatalf("optional excluded from counts: done=%d pending=%d na=%d want 1/0/0", done, pending, na)
	}
}

func TestCloseCounts(t *testing.T) {
	done, pending, na := progress.CloseCounts("## Progress\n- [x] a\n- [ ] b\n- [ ] c\nprose\n* [X] d\n- [~] e")
	if done != 2 || pending != 2 || na != 1 {
		t.Fatalf("done=%d pending=%d na=%d want 2/2/1", done, pending, na)
	}
	if d, p, n := progress.CloseCounts("no checklist here"); d != 0 || p != 0 || n != 0 {
		t.Fatalf("empty content done=%d pending=%d na=%d want 0/0/0", d, p, n)
	}
}

func TestMemoryStore_roundtrip(t *testing.T) {
	s := progress.NewMemoryStore()
	s.Set("root-1", "- [ ] a")
	if got := s.Get(t.Context(), "root-1"); got != "- [ ] a" {
		t.Fatalf("Get = %q", got)
	}
	if got := s.Get(t.Context(), "other"); got != "" {
		t.Fatalf("unset session Get = %q want empty", got)
	}
}

func TestBumpRevision_monotonic(t *testing.T) {
	key := uniqueProgressKey("progress-rev-monotonic")
	if got := progress.BumpRevision(key); got != 1 {
		t.Fatalf("first bump = %d want 1", got)
	}
	if got := progress.BumpRevision(key); got != 2 {
		t.Fatalf("second bump = %d want 2", got)
	}
	if got := progress.CurrentRevision(key); got != 2 {
		t.Fatalf("get = %d want 2", got)
	}
}
