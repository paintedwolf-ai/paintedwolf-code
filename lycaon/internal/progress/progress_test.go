package progress_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeriveProgressProjectsRowsAndDropsOptional(t *testing.T) {
	rows := []progress.Row{
		{ID: "p1", State: progress.ProgressStatePending, Label: "first"},
		{ID: "p2", State: progress.ProgressStateDone, Label: "second"},
		{ID: "p3", State: progress.ProgressStateNA, Label: "third"},
		{ID: "p4", State: progress.ProgressStateOptional, Label: "synthesize report"},
	}
	res := progress.DeriveProgress(rows, progress.DefaultProgressCap)
	if len(res.Items) != 3 {
		t.Fatalf("items = %d want 3 (%+v)", len(res.Items), res.Items)
	}
	if res.Items[0].State != progress.ProgressStatePending || res.Items[0].Label != "first" {
		t.Fatalf("item0 = %+v", res.Items[0])
	}
	if res.Items[1].State != progress.ProgressStateDone || res.Items[2].State != progress.ProgressStateNA {
		t.Fatalf("states = %+v", res.Items)
	}
}

func TestProgressMissing(t *testing.T) {
	if !progress.ProgressMissing(nil) {
		t.Fatal("expected plan missing when rows are nil")
	}
	if progress.ProgressMissing([]progress.Row{{ID: "p1", State: progress.ProgressStatePending, Label: "do the thing"}}) {
		t.Fatal("expected plan present with a row")
	}
}

func TestDeriveProgressClampsOverlongLabel(t *testing.T) {
	long := strings.Repeat("b", progress.MaxLabelRunes+10)
	res := progress.DeriveProgress([]progress.Row{{ID: "p1", State: progress.ProgressStatePending, Label: long}}, 0)
	if len(res.Items) != 1 {
		t.Fatalf("items = %d want 1", len(res.Items))
	}
	if n := len([]rune(res.Items[0].Label)); n > progress.MaxLabelRunes+1 {
		t.Fatalf("label not clamped: %d runes", n)
	}
}

func TestDeriveProgressSupportsFortyEightRows(t *testing.T) {
	rows := make([]progress.Row, progress.MaxProgressCap)
	for i := range rows {
		rows[i] = progress.Row{ID: "p1", State: progress.ProgressStatePending, Label: "step"}
	}
	res := progress.DeriveProgress(rows, progress.MaxProgressCap)
	if len(res.Items) != 48 || res.More != 0 {
		t.Fatalf("DeriveProgress = %d items + %d more, want 48 + 0", len(res.Items), res.More)
	}

	overflow := append(rows, progress.Row{ID: "p49", State: progress.ProgressStatePending, Label: "overflow"})
	res = progress.DeriveProgress(overflow, progress.MaxProgressCap+1)
	if len(res.Items) != 48 || res.More != 1 {
		t.Fatalf("capped DeriveProgress = %d items + %d more, want 48 + 1", len(res.Items), res.More)
	}
}

func TestSummarizeUpdateCountsCurrentStates(t *testing.T) {
	steps := progress.DeriveProgress([]progress.Row{
		{ID: "p1", State: progress.ProgressStatePending, Label: "a"},
		{ID: "p2", State: progress.ProgressStateDone, Label: "b"},
		{ID: "p3", State: progress.ProgressStateNA, Label: "c"},
	}, 0).Items
	got := progress.SummarizeUpdate(steps, 17)
	if got.ChangeCount != 17 || got.TotalSteps != 3 || got.Pending != 1 || got.Done != 1 || got.NA != 1 {
		t.Fatalf("SummarizeUpdate = %+v", got)
	}
}

func TestAllTerminalAndOpenSteps(t *testing.T) {
	if progress.HasOpenSteps(nil) || progress.AllTerminal(nil) {
		t.Fatal("empty rows must have no open steps and not be terminal")
	}
	doneAndNA := []progress.Row{
		{ID: "p1", State: progress.ProgressStateDone, Label: "a"},
		{ID: "p2", State: progress.ProgressStateNA, Label: "b"},
		{ID: "p3", State: progress.ProgressStateOptional, Label: "opt"},
	}
	if !progress.AllTerminal(doneAndNA) || progress.HasOpenSteps(doneAndNA) {
		t.Fatal("done + na + optional should be terminal with zero open steps")
	}
	withOpen := append(doneAndNA[:2:2], progress.Row{ID: "p3", State: progress.ProgressStatePending, Label: "c"})
	if progress.AllTerminal(withOpen) || !progress.HasOpenSteps(withOpen) || progress.OpenStepCount(withOpen) != 1 {
		t.Fatal("pending row must block completion and count as 1 open step")
	}
	done, pending, na := progress.CloseCounts(withOpen)
	if done != 1 || pending != 1 || na != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/1", done, pending, na)
	}
}

func TestRenderMarkdownAndBuildDigest(t *testing.T) {
	s := progresstest.New(t, "root-1")
	out, err := s.Apply(t.Context(), "root-1", progress.Mutation{
		Replace: []progress.RowSpec{
			{Label: "a", State: progress.ProgressStatePending},
			{Label: "b", State: progress.ProgressStateOptional},
		},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "apply initial rows", err)
	if out.Snapshot.RenderMarkdown() != "- [ ] a\n- [>] b" {
		t.Fatalf("RenderMarkdown = %q", out.Snapshot.RenderMarkdown())
	}
	digest := progress.BuildDigest(out.Snapshot)
	if digest.Revision != 1 || len(digest.Steps) != 1 || digest.Steps[0].Label != "a" {
		t.Fatalf("BuildDigest = %+v", digest)
	}
	other, err := s.Snapshot(t.Context(), "other")
	testutil.FailErr(t, "snapshot unset session", err)
	if !other.Missing() || progress.BuildDigest(other).Revision != 0 {
		t.Fatalf("unset snapshot = %+v", other)
	}
}
