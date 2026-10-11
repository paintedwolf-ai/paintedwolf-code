package sourceapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSymbolExecutorFiltersBeforeRetainingBoundedResults(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	var source strings.Builder
	source.WriteString("package p\n")
	for i := range 201 {
		fmt.Fprintf(&source, "func TargetExcluded%03d() {}\n", i)
	}
	source.WriteString("func TargetWanted() {}\n")
	root := p.Roots[0]
	testutil.FailErr(t, "write filtered declarations", os.WriteFile(filepath.Join(root.Path, "f000.go"), []byte(source.String()), 0600))
	repochange.Advance(root.Path)
	sourcecatalog.Process().InvalidateRootChange(root.Path, []string{"f000.go"}, repochange.StructuralPathSet{})
	leg.Flags.WholeWord = false
	leg.Query = search.AndExpr{Exprs: []search.Node{
		search.TextExpr{Text: "Target"},
		search.NotExpr{Expr: search.TextExpr{Text: "Excluded"}},
	}}
	for attempt := range 2 {
		result, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
		testutil.FailErr(t, "find admitted declaration", err)
		if len(result.Hits) != 1 || result.Hits[0].Title != "TargetWanted" || len(result.Issues) != 0 {
			t.Fatalf("attempt %d: filtered declarations consumed retained result capacity: %+v", attempt, result)
		}
	}
}

func TestSymbolExecutorExcludedExactNameDoesNotStopAbbreviations(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 2)
	root := p.Roots[0]
	for name, content := range map[string]string{
		"f000.go": "package p\nfunc PC() {}\n",
		"f001.go": "package p\nfunc ParseConfig() {}\n",
	} {
		testutil.FailErr(t, "write abbreviation fixture", os.WriteFile(filepath.Join(root.Path, name), []byte(content), 0600))
	}
	repochange.Advance(root.Path)
	sourcecatalog.Process().InvalidateRootChange(root.Path, []string{"f000.go", "f001.go"}, repochange.StructuralPathSet{})
	leg.Name = "pc"
	leg.Flags.WholeWord = false
	leg.Query = search.AndExpr{Exprs: []search.Node{
		search.TextExpr{Text: "pc"},
		search.NotExpr{Expr: search.OrExpr{Exprs: []search.Node{
			search.FilterExpr{Field: "path", Value: "f000.go"},
			search.FilterExpr{Field: "path", Value: "other.go"},
		}}},
	}}
	for attempt := range 2 {
		result, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
		testutil.FailErr(t, "find admitted abbreviation", err)
		if len(result.Hits) != 1 || result.Hits[0].Title != "ParseConfig" || len(result.Issues) != 0 {
			t.Fatalf("attempt %d: excluded exact name stopped abbreviation discovery: %+v", attempt, result)
		}
	}
}

func symbolProgressFixture(t *testing.T, count int) (*SymbolExecutor, *project.Project, *search.SymbolPlanLeg) {
	t.Helper()
	root := t.TempDir()
	for i := range count {
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte("package p\nfunc Target() {}\n"), 0600))
	}
	registry := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), registry, root)
	testutil.FailErr(t, "create project", err)
	testutil.FailErr(t, "prepare index", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, sourcecatalog.Root{ID: p.Roots[0].ID, Path: p.Roots[0].Path}))
	leg := &search.SymbolPlanLeg{Name: "Target", Query: search.TextExpr{Text: "Target"}, Roots: []search.CodeRoot{{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: p.Roots[0].Path}}, Cap: 200, Flags: search.MatchFlags{WholeWord: true}}
	return NewSymbolExecutor(registry), p, leg
}

func TestSymbolExecutorAdvancesThenInvalidatesContent(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 60)
	run := func() search.ExecutorReport {
		t.Helper()
		result, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
		testutil.FailErr(t, "run symbols", err)
		return result
	}
	first := run()
	if len(first.Hits) != 48 {
		t.Fatalf("first=%+v", first)
	}
	var firstEntries []string
	for key, entry := range e.progress.entries {
		firstEntries = append(firstEntries, fmt.Sprintf("%x %s", key, entry.stamp))
	}
	second := run()
	if len(second.Hits) != 60 || len(second.Issues) != 0 {
		var nextEntries []string
		for key, entry := range e.progress.entries {
			nextEntries = append(nextEntries, fmt.Sprintf("%x %s", key, entry.stamp))
		}
		t.Fatalf("second hits=%d issues=%+v first=%v next=%v", len(second.Hits), second.Issues, firstEntries, nextEntries)
	}
	before, err := symbolStamp(t.Context(), p, nil)
	testutil.FailErr(t, "initial stamp", err)
	root := p.Roots[0].Path
	testutil.FailErr(t, "change source bytes", os.WriteFile(filepath.Join(root, "f000.go"), []byte("package p\nfunc Others() {}\n"), 0600))
	repochange.Advance(root)
	sourcecatalog.Process().InvalidateRootChange(root, []string{"f000.go"}, repochange.StructuralPathSet{})
	after, err := symbolStamp(t.Context(), p, nil)
	testutil.FailErr(t, "changed stamp", err)
	var oldStamp, newStamp []symbolSourceStamp
	testutil.FailErr(t, "decode old stamp", json.Unmarshal([]byte(before), &oldStamp))
	testutil.FailErr(t, "decode new stamp", json.Unmarshal([]byte(after), &newStamp))
	if oldStamp[0].Revision != newStamp[0].Revision || symbolEpochsCurrent(before) {
		t.Fatalf("content change should preserve membership and invalidate old epochs: %s => %s", before, after)
	}
	changed := run()
	for _, hit := range changed.Hits {
		if hit.Path == "f000.go" {
			t.Fatalf("stale declaration=%+v", hit)
		}
	}
	if len(changed.Hits) != 48 {
		t.Fatalf("changed source did not restart bounded progress: %d", len(changed.Hits))
	}
}

func TestSymbolProgressCacheBindsQueryAndExpires(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	acquire := func() *symbolProgressEntry {
		t.Helper()
		entry, release, err := e.progress.acquire(t.Context(), p, leg, nil, true)
		testutil.FailErr(t, "acquire progress", err)
		release()
		return entry
	}
	first := acquire()
	leg.Budget = search.BudgetInteractive
	if acquire() != first {
		t.Fatal("budget refinement forked query progress")
	}
	leg.Query = search.AndExpr{Exprs: []search.Node{search.FilterExpr{Field: "path", Value: "a"}, search.FilterExpr{Field: "path", Value: "b"}}}
	and := acquire()
	leg.Query = search.OrExpr{Exprs: []search.Node{search.FilterExpr{Field: "path", Value: "a"}, search.FilterExpr{Field: "path", Value: "b"}}}
	or := acquire()
	if and == or {
		t.Fatal("AND and OR shared progress")
	}
	or.used = time.Now().Add(-symbolProgressTTL)
	if acquire() == or {
		t.Fatal("expired progress survived")
	}
	for i := range symbolProgressLimit + 2 {
		leg.Name = fmt.Sprint(i)
		acquire()
	}
	if len(e.progress.entries) > symbolProgressLimit {
		t.Fatalf("cache entries=%d", len(e.progress.entries))
	}
}

func TestSymbolProgressWaitCancellationPreservesEntry(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	first, release, err := e.progress.acquire(t.Context(), p, leg, nil, true)
	testutil.FailErr(t, "hold progress", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = e.progress.acquire(ctx, p, leg, nil, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting error=%v", err)
	}
	release()
	next, done, err := e.progress.acquire(t.Context(), p, leg, nil, true)
	testutil.FailErr(t, "resume progress", err)
	defer done()
	if first != next {
		t.Fatal("canceled waiter evicted progress")
	}
}

func TestSymbolProgressInvalidatesMembership(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	first, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "initial search", err)
	if len(first.Hits) != 1 {
		t.Fatalf("first hits=%d issues=%+v", len(first.Hits), first.Issues)
	}
	root := p.Roots[0]
	testutil.FailErr(t, "add source", os.WriteFile(filepath.Join(root.Path, "new.go"), []byte("package p\nfunc Target() {}\n"), 0600))
	sourcecatalog.Process().InvalidateRoot(root.Path, "new.go")
	testutil.FailErr(t, "prepare new generation", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}))
	next, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "new generation search", err)
	if len(next.Hits) != 2 || len(next.Issues) != 0 {
		t.Fatalf("new generation hits=%d issues=%+v", len(next.Hits), next.Issues)
	}
}
