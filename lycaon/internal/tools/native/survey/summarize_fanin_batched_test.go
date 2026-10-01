package survey

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fanInTieFixture(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "alpha/a.go", "package alpha\nfunc A() {}\n")
	writeFile(t, dir, "beta/b.go", "package beta\nfunc B() {}\n")
	writeFile(t, dir, "use/u1.go", "package use\n\nimport \"example.com/app/beta\"\n\nfunc U1() { _ = beta.B }\n")
	writeFile(t, dir, "use/u2.go", "package use\n\nimport \"example.com/app/beta\"\n\nfunc U2() { _ = beta.B }\n")
	writeFile(t, dir, "use/u3.go", "package use\n\nimport \"example.com/app/beta\"\n\nfunc U3() { _ = beta.B }\n")
	writeFile(t, dir, "other/o.go", "package other\n\nimport \"example.com/app/alpha\"\n\nfunc O() { _ = alpha.A }\n")
}

func fanInImportanceScore(imp summarize.SubtreeImportance, prefix string) int {
	if imp == nil {
		return 0
	}
	for path, c := range imp {
		if path == prefix || path == prefix+"/b.go" || path == prefix+"/a.go" {
			return c.DocLinks + c.FanIn
		}
	}
	return 0
}

func TestFanInSingleBatchedPass(t *testing.T) {
	dir := t.TempDir()
	fanInTieFixture(t, dir)

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeDoclinkMax = 0
	caps.Pack.SubtreeFaninMax = 64
	caps.Pack.SubtreeFaninGrepMax = 1
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain"})
	testutil.FailErr(t, "gather", err)

	if res.Stats.FaninGrepPasses > 1 {
		t.Fatalf("fanin_grep_passes = %d, want ≤ 1", res.Stats.FaninGrepPasses)
	}
	if res.Stats.FaninGrepPasses == 0 {
		t.Fatal("expected one batched fan-in grep pass")
	}
	if res.Importance == nil {
		t.Fatal("expected fan-in importance")
	}
	beta := fanInImportanceScore(res.Importance, "beta")
	alpha := fanInImportanceScore(res.Importance, "alpha")
	if beta <= alpha {
		t.Fatalf("beta fan-in = %d should beat alpha %d; imp=%+v", beta, alpha, res.Importance)
	}
}

func TestFanInParityWith160(t *testing.T) {
	dir := t.TempDir()
	fanInTieFixture(t, dir)

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeDoclinkMax = 0
	caps.Pack.SubtreeFaninMax = 64
	caps.Pack.SubtreeFaninGrepMax = 1
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain"})
	testutil.FailErr(t, "gather", err)

	beta := fanInImportanceScore(res.Importance, "beta")
	alpha := fanInImportanceScore(res.Importance, "alpha")
	if beta <= alpha {
		t.Fatalf("beta should win tie; beta=%d alpha=%d imp=%+v", beta, alpha, res.Importance)
	}
	if res.Stats.FaninGrepPasses != 1 {
		t.Fatalf("fanin_grep_passes = %d, want 1", res.Stats.FaninGrepPasses)
	}
}

func TestFanInGrepCapDisables(t *testing.T) {
	dir := t.TempDir()
	fanInTieFixture(t, dir)

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeFaninMax = 64
	caps.Pack.SubtreeFaninGrepMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain"})
	testutil.FailErr(t, "gather", err)

	if res.Stats.FaninGrepPasses != 0 {
		t.Fatalf("fanin_grep_passes = %d, want 0 when cap disabled", res.Stats.FaninGrepPasses)
	}
	for _, c := range res.Importance {
		if c.FanIn > 0 {
			t.Fatalf("grep cap=0 must not accrue fan-in; got %+v", res.Importance)
		}
	}
}

func TestFanInContentionBandOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	// Unequal immediate denseness (shallow Files counts) — not deep def counts.
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("parent/big/b%d.go", i), "package big\nfunc B() {}\n")
	}
	writeFile(t, dir, "parent/small/s.go", "package small\nfunc S() {}\n")
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("importers/i%d.go", i), "package imp\n\nimport \"example.com/app/parent/big\"\n\nfunc F() { _ = big.B }\n")
	}

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeFaninMax = 64
	caps.Pack.SubtreeFaninGrepMax = 1
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "parent", Task: "explain"})
	testutil.FailErr(t, "gather", err)

	if res.Stats.FaninGrepPasses != 0 {
		t.Fatalf("unequal-material siblings should skip fan-in grep; passes=%d imp=%+v", res.Stats.FaninGrepPasses, res.Importance)
	}
}

func TestFanInNoPileExpansion(t *testing.T) {
	dir := t.TempDir()
	fanInTieFixture(t, dir)

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeFaninMax = 64
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain"})
	testutil.FailErr(t, "gather", err)

	for _, p := range structureFilePaths(res.Structure) {
		if p == "ghost/never.go" {
			t.Fatal("fan-in must not expand gather pile")
		}
	}
}
