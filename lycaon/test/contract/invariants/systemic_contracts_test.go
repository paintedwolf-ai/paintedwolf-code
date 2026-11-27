package invariants_test

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

var forcedGCTree = map[string]string{
	"lycaon/pkg/testcorpus/corpus.go":            "package testcorpus\n\nfunc Load() {}\n",
	"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport \"testing\"\n\nfunc TestClean(t *testing.T) {}\n",
}

// Explicit runtime.GC and runtime/debug.FreeOSMemory are banned in ordinary
// contract suites and their shared analysis helpers, including aliased and dot
// imports, while shadowed locals, unrelated same-named methods, and genuine
// collector-behavior tests outside contract suites remain allowed.
func TestContractSuitesAvoidForcedGC(t *testing.T) {
	t.Parallel()
	gcRule := rule(t, forbiddencalls.Family, "contract-suites-avoid-forced-gc")
	for name, tc := range map[string]struct {
		overrides map[string]string
		want      outcome
		wantPos   string
	}{
		"clean contract suite": {want: passes},
		"seeded runtime.GC in contract test": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"runtime\"\n\t\"testing\"\n)\n\nfunc TestSeeded(t *testing.T) {\n\truntime.GC()\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"aliased runtime import in shared helper": {
			overrides: map[string]string{
				"lycaon/pkg/testcorpus/corpus.go": "package testcorpus\n\nimport rt \"runtime\"\n\nfunc Load() { rt.GC() }\n",
			},
			want:    violates,
			wantPos: "pkg/testcorpus/corpus.go:5:",
		},
		"debug.FreeOSMemory in contract helper": {
			overrides: map[string]string{
				"lycaon/test/contract/internal/helper/helper.go": "package helper\n\nimport \"runtime/debug\"\n\nfunc Scavenge() { debug.FreeOSMemory() }\n",
			},
			want:    violates,
			wantPos: "test/contract/internal/helper/helper.go:5:",
		},
		"aliased debug.FreeOSMemory in contract test": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport dbg \"runtime/debug\"\n\nfunc helper() { dbg.FreeOSMemory() }\n",
			},
			want:    violates,
			wantPos: "test/contract/sample/sample_test.go:5:",
		},
		"dot-imported runtime.GC": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport . \"runtime\"\n\nfunc helper() { GC() }\n",
			},
			want:    violates,
			wantPos: "test/contract/sample/sample_test.go:5:",
		},
		"local GC function shadowing dot-imported runtime": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport . \"runtime\"\n\nfunc helper() {\n\t_ = GOOS\n\tGC := func() {}\n\tGC()\n}\n",
			},
			want: passes,
		},
		"dot-imported runtime.GC outside inner shadow block": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport . \"runtime\"\n\nfunc helper() {\n\t{\n\t\tGC := func() {}\n\t\tGC()\n\t}\n\tGC()\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/sample/sample_test.go:10:",
		},
		"if-initializer shadows runtime import only inside if and genuine call after is caught": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport \"runtime\"\n\ntype localGC struct{}\n\nfunc (localGC) GC() {}\n\nfunc helper() {\n\tif runtime := (localGC{}); true {\n\t\truntime.GC()\n\t}\n\truntime.GC()\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/sample/sample_test.go:13:",
		},
		"if-initializer shadows dot-imported GC only inside if and genuine call after is caught": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport . \"runtime\"\n\nfunc helper() {\n\tif GC := func() {}; true {\n\t\tGC()\n\t}\n\tGC()\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"shadowed runtime and debug identifiers": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"runtime\"\n\t\"runtime/debug\"\n)\n\ntype localGC struct{}\n\nfunc (localGC) GC()           {}\nfunc (localGC) FreeOSMemory() {}\n\nfunc realUse() string {\n\t_, _ = runtime.GOOS, debug.SetMemoryLimit\n\treturn runtime.GOOS\n}\n\nfunc shadowed(runtime localGC) {\n\tdebug := localGC{}\n\truntime.GC()\n\tdebug.FreeOSMemory()\n}\n",
			},
			want: passes,
		},
		"unrelated same-named methods and packages": {
			overrides: map[string]string{
				"lycaon/internal/other/other.go": "package other\n\nfunc GC() {}\nfunc FreeOSMemory() {}\n",
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport \"github.com/lycaon/lycaon/internal/other\"\n\ntype local struct{}\n\nfunc (local) GC() {}\nfunc (local) FreeOSMemory() {}\n\nfunc helper(x local) {\n\tx.GC()\n\tx.FreeOSMemory()\n\tother.GC()\n\tother.FreeOSMemory()\n}\n",
			},
			want: passes,
		},
		"permitted collector-behavior and profiling tests outside contract suites": {
			overrides: map[string]string{
				"lycaon/test/wiring/host_release_test.go":          "package wiring\n\nimport \"runtime\"\n\nfunc release() { runtime.GC() }\n",
				"lycaon/test/scanstress/leak_test.go":              "package scanstress\n\nimport \"runtime\"\n\nfunc checkLeak() { runtime.GC() }\n",
				"lycaon/internal/testutil/resourceguard/guard.go":  "package resourceguard\n\nimport \"runtime/debug\"\n\nfunc Check() { debug.FreeOSMemory() }\n",
				"lycaon/internal/sourcecatalog/index_bench_test.go": "package sourcecatalog\n\nimport \"runtime\"\n\nfunc bench() { runtime.GC() }\n",
			},
			want: passes,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(fixture(t, with(forcedGCTree, tc.overrides)), gcRule)
			expect(t, res, tc.want, 1)
			if tc.wantPos != "" && !strings.HasPrefix(res.Violations[0], tc.wantPos) {
				t.Fatalf("violation = %q, want prefix %q", res.Violations[0], tc.wantPos)
			}
		})
	}
}

var onceMemoTree = map[string]string{
	"lycaon/test/contract/invariants/corpus.go": "package invariants\n\nimport \"sync\"\n\ntype onceValue[T any] struct {\n\tonce  sync.Once\n\tvalue T\n\terr   error\n}\n\nfunc (o *onceValue[T]) get(load func() (T, error)) (T, error) {\n\to.once.Do(func() { o.value, o.err = load() })\n\treturn o.value, o.err\n}\n\ntype Corpus struct {\n\ttyped onceValue[*Program]\n}\n\ntype Program struct{}\n",
	"lycaon/pkg/testcorpus/corpus.go":           "package testcorpus\n\nimport \"sync\"\n\ntype cacheEntry struct {\n\tonce   sync.Once\n\tcorpus *Corpus\n\terr    error\n}\n\ntype Corpus struct{}\n",
	"lycaon/pkg/testcorpus/go.go":               "package testcorpus\n\nimport \"sync\"\n\ntype goCacheEntry struct {\n\tonce   sync.Once\n\tcorpus *GoCorpus\n\terr    error\n}\n\ntype GoCorpus struct{}\n",
}

// Pure once-initialized analysis memos must not be overwritten outside once.Do,
// even if a function attempts an unguarded sync.Once reassignment; renamed
// fields, distinct result types, caller-local releases, mutex-coordinated
// resettable caches, and unrelated sync.Once uses are all handled accurately.
func TestSharedOnceMemoOwnership(t *testing.T) {
	t.Parallel()
	memoRule := rule(t, forbiddencalls.Family, "shared-once-memo-results-stay-immutable")
	for name, tc := range map[string]struct {
		overrides map[string]string
		want      outcome
		wantPos   string
	}{
		"clean memos": {want: passes},
		"seeded write to nested onceValue field outside once.Do": {
			overrides: map[string]string{
				"lycaon/test/contract/invariants/release.go": "package invariants\n\nfunc (c *Corpus) Release() {\n\tc.typed.value = nil\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/invariants/release.go:4:",
		},
		"seeded write to direct onceValue field in clear method": {
			overrides: map[string]string{
				"lycaon/test/contract/invariants/clear.go": "package invariants\n\nfunc (o *onceValue[T]) Clear() {\n\tvar zero T\n\to.value = zero\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/invariants/clear.go:5:",
		},
		"unguarded sync.Once reset in branch does not exempt write": {
			overrides: map[string]string{
				"lycaon/test/contract/invariants/unsafe_reset.go": "package invariants\n\nimport \"sync\"\n\nfunc (o *onceValue[T]) UnsafeReset(cond bool) {\n\tif cond {\n\t\to.once = sync.Once{}\n\t}\n\tvar zero T\n\to.value = zero\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/invariants/unsafe_reset.go:10:",
		},
		"renamed fields and distinct result type with aliased sync import": {
			overrides: map[string]string{
				"lycaon/test/contract/internal/cache/memo.go": "package cache\n\nimport s \"sync\"\n\ntype Index struct{}\n\ntype indexMemo struct {\n\tinitOnce s.Once\n\tsnapshot *Index\n}\n\nfunc (m *indexMemo) Get() *Index {\n\tm.initOnce.Do(func() { m.snapshot = &Index{} })\n\treturn m.snapshot\n}\n\nfunc (m *indexMemo) Drop() {\n\tm.snapshot = nil\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/internal/cache/memo.go:18:",
		},
		"distinct receiver written inside another instance once.Do": {
			overrides: map[string]string{
				"lycaon/test/contract/internal/cache/distinct.go": "package cache\n\nimport \"sync\"\n\ntype Index struct{}\n\ntype indexMemo struct {\n\tinitOnce sync.Once\n\tsnapshot *Index\n}\n\nfunc (first *indexMemo) Corrupt(second *indexMemo) {\n\tfirst.initOnce.Do(func() {\n\t\tfirst.snapshot = &Index{}\n\t\tsecond.snapshot = nil\n\t})\n}\n",
			},
			want:    violates,
			wantPos: "test/contract/internal/cache/distinct.go:15:",
		},
		"legitimate caller-local release": {
			overrides: map[string]string{
				"lycaon/test/contract/internal/cache/local.go": "package cache\n\nimport \"sync\"\n\ntype Index struct{}\n\ntype indexMemo struct {\n\tinitOnce sync.Once\n\tsnapshot *Index\n}\n\nfunc (m *indexMemo) Get() *Index {\n\tm.initOnce.Do(func() { m.snapshot = &Index{} })\n\treturn m.snapshot\n}\n\nfunc consume(m *indexMemo) {\n\tsnap := m.Get()\n\t_ = snap\n\tsnap = nil\n}\n",
			},
			want: passes,
		},
		"independently resettable cache coordinated by mutex": {
			overrides: map[string]string{
				"lycaon/test/contract/internal/cache/resettable.go": "package cache\n\nimport \"sync\"\n\ntype Index struct{}\n\ntype resettableMemo struct {\n\tmu       sync.Mutex\n\tready    bool\n\tsnapshot *Index\n}\n\nfunc (m *resettableMemo) Get() *Index {\n\tm.mu.Lock()\n\tdefer m.mu.Unlock()\n\tif !m.ready {\n\t\tm.snapshot = &Index{}\n\t\tm.ready = true\n\t}\n\treturn m.snapshot\n}\n\nfunc (m *resettableMemo) Reset() {\n\tm.mu.Lock()\n\tdefer m.mu.Unlock()\n\tm.ready = false\n\tm.snapshot = nil\n}\n",
			},
			want: passes,
		},
		"unrelated sync.Once synchronization": {
			overrides: map[string]string{
				"lycaon/test/contract/internal/cache/notify.go": "package cache\n\nimport \"sync\"\n\ntype notifier struct {\n\tonce  sync.Once\n\tch    chan struct{}\n\tcount int\n}\n\nfunc (n *notifier) Notify() { n.once.Do(func() { close(n.ch) }) }\nfunc (n *notifier) Bump()   { n.count++ }\n",
			},
			want: passes,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(fixture(t, with(onceMemoTree, tc.overrides)), memoRule)
			expect(t, res, tc.want, 1)
			if tc.wantPos != "" && !strings.HasPrefix(res.Violations[0], tc.wantPos) {
				t.Fatalf("violation = %q, want prefix %q", res.Violations[0], tc.wantPos)
			}
		})
	}
}

// verifySharedMemoLifetime is a reusable behavioral contract asserting that
// once a shared analysis cache publishes a result, repeated and concurrent
// consumers observe a usable value until its defined lifetime ends.
func verifySharedMemoLifetime[T any](
	get func() (T, error),
	usable func(T) bool,
	afterFirst func(),
) error {
	first, err := get()
	if err != nil {
		return fmt.Errorf("initial publish failed: %w", err)
	}
	if !usable(first) {
		return fmt.Errorf("initial published value is unusable")
	}
	if afterFirst != nil {
		afterFirst()
	}
	var (
		wg   sync.WaitGroup
		errs = make(chan error, 8)
	)
	for range 8 {
		wg.Go(func() {
			got, err := get()
			if err != nil {
				errs <- fmt.Errorf("subsequent consumer failed: %w", err)
				return
			}
			if !usable(got) {
				errs <- fmt.Errorf("subsequent consumer observed unusable result after publication")
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	return nil
}

// Reusable lifetime behavioral contract: verifies live shared caches keep
// published results usable across repeated and concurrent consumers, allows
// caller-local releases and mutex-coordinated resets, and catches a seeded
// cache whose result is cleared after publication.
func TestSharedMemoLifetimeBehavioralContract(t *testing.T) {
	t.Parallel()

	t.Run("invariants corpus remains usable across concurrent consumers", func(t *testing.T) {
		t.Parallel()
		corpus := fixture(t, forcedGCTree)
		err := verifySharedMemoLifetime(
			func() (*invariants.GoCorpus, error) { return corpus.Go(), nil },
			func(g *invariants.GoCorpus) bool { return g != nil && len(g.Files()) > 0 },
			nil,
		)
		contractcheck.FailErr(t, "invariants.Corpus lifetime", err)
		if stats := corpus.Stats(); stats.GoParses != 1 {
			t.Fatalf("GoParses = %d, want 1", stats.GoParses)
		}
	})

	t.Run("testcorpus.GoLoader remains usable across concurrent consumers", func(t *testing.T) {
		t.Parallel()
		corpus := fixture(t, forcedGCTree)
		var loader testcorpus.GoLoader
		err := verifySharedMemoLifetime(
			func() (*testcorpus.GoCorpus, error) {
				return loader.Load(corpus.Path("lycaon/test/contract"), testcorpus.Options{}, 0)
			},
			func(g *testcorpus.GoCorpus) bool { return g != nil && len(g.Files()) > 0 },
			nil,
		)
		contractcheck.FailErr(t, "testcorpus.GoLoader lifetime", err)
	})

	t.Run("seeded post-publication clear is caught", func(t *testing.T) {
		t.Parallel()
		var once sync.Once
		var published *string
		get := func() (*string, error) {
			once.Do(func() {
				s := "live-analysis"
				published = &s
			})
			return published, nil
		}
		err := verifySharedMemoLifetime(
			get,
			func(v *string) bool { return v != nil && *v != "" },
			func() { published = nil },
		)
		if err == nil {
			t.Fatal("expected lifetime contract to catch post-publication clear")
		}
	})

	t.Run("independently resettable mutex cache republishes usable value", func(t *testing.T) {
		t.Parallel()
		var (
			mu    sync.Mutex
			loads atomic.Int64
			val   *string
		)
		get := func() (*string, error) {
			mu.Lock()
			defer mu.Unlock()
			if val == nil {
				s := fmt.Sprintf("gen-%d", loads.Add(1))
				val = &s
			}
			return val, nil
		}
		reset := func() {
			mu.Lock()
			defer mu.Unlock()
			val = nil
		}
		err := verifySharedMemoLifetime(
			get,
			func(v *string) bool { return v != nil && *v != "" },
			reset,
		)
		contractcheck.FailErr(t, "resettable cache lifetime", err)
		if loads.Load() != 2 {
			t.Fatalf("loads = %d, want 2 (initial + republished after coordinated reset)", loads.Load())
		}
	})
}

// Behavioral contract 1: repeated runs of shared analysis consumers in one
// process across different rule orders and subsets must produce identical
// violations, source locations, and counts while parsing and type-checking
// at most once. Seeded violations ensure two empty outputs cannot pass.
func TestSharedAnalysisRepeatedRunsPreserveFindingsAndLoadOnce(t *testing.T) {
	t.Parallel()
	files := with(with(onceMemoTree, pongoStub), map[string]string{
		"lycaon/internal/fixture/render.go": "package fixture\n\nimport \"github.com/flosch/pongo2/v6\"\n\n" +
			"func example(tpl *pongo2.Template) {\n\t_, _ = tpl.Execute(nil)\n}\n",
		"lycaon/test/contract/sample/seeded_test.go": "package sample\n\nimport rt \"runtime\"\n\nfunc seeded() { rt.GC() }\n",
	})
	corpus := fixture(t, files)

	gcRule := rule(t, forbiddencalls.Family, "contract-suites-avoid-forced-gc")
	pongoRule := rule(t, forbiddencalls.Family, "pongo-templates-carry-the-host-ceiling")
	memoRule := rule(t, forbiddencalls.Family, "shared-once-memo-results-stay-immutable")

	type snapshot struct {
		violations []string
		failed     bool
	}
	evalAll := func(order []invariants.Invariant) map[string]snapshot {
		out := make(map[string]snapshot, len(order))
		for _, r := range order {
			res := invariants.Evaluate(corpus, r)
			if res.Err != nil || len(res.MissingAnchors) > 0 || len(res.StaleExemptions) > 0 {
				t.Fatalf("rule %s unexpected non-violation failure:\n%s", r.Name, res)
			}
			out[r.Name] = snapshot{violations: slices.Clone(res.Violations), failed: res.Failed()}
		}
		return out
	}

	forward := evalAll([]invariants.Invariant{gcRule, pongoRule, memoRule})
	reversed := evalAll([]invariants.Invariant{memoRule, pongoRule, gcRule})
	subsetA := evalAll([]invariants.Invariant{pongoRule})
	subsetB := evalAll([]invariants.Invariant{gcRule, memoRule})

	// Seeded violations must be non-empty so two empty runs cannot pass.
	if len(forward[gcRule.Name].violations) != 1 || !strings.HasPrefix(forward[gcRule.Name].violations[0], "test/contract/sample/seeded_test.go:5:") {
		t.Fatalf("gcRule violations = %v, want seeded violation at line 5", forward[gcRule.Name].violations)
	}
	if len(forward[pongoRule.Name].violations) != 1 || !strings.HasPrefix(forward[pongoRule.Name].violations[0], "internal/fixture/render.go:6:") {
		t.Fatalf("pongoRule violations = %v, want seeded violation at line 6", forward[pongoRule.Name].violations)
	}
	if len(forward[memoRule.Name].violations) != 0 {
		t.Fatalf("memoRule violations = %v, want 0", forward[memoRule.Name].violations)
	}

	for name, want := range forward {
		if !slices.Equal(reversed[name].violations, want.violations) || reversed[name].failed != want.failed {
			t.Fatalf("reversed run drifted for %s: got %+v, want %+v", name, reversed[name], want)
		}
	}
	if !slices.Equal(subsetA[pongoRule.Name].violations, forward[pongoRule.Name].violations) {
		t.Fatalf("subsetA drifted for %s: got %v, want %v", pongoRule.Name, subsetA[pongoRule.Name].violations, forward[pongoRule.Name].violations)
	}
	for _, name := range []string{gcRule.Name, memoRule.Name} {
		if !slices.Equal(subsetB[name].violations, forward[name].violations) {
			t.Fatalf("subsetB drifted for %s: got %v, want %v", name, subsetB[name].violations, forward[name].violations)
		}
	}
	if stats := corpus.Stats(); stats.GoParses != 1 || stats.TypeChecks != 1 {
		t.Fatalf("corpus.Stats() = %+v, want GoParses=1 TypeChecks=1", stats)
	}

	// Also confirm shared contractcheck.LoadGoASTCorpus reuses the underlying
	// FileSet, parsed ASTs, paths, and observable positions across repeated
	// calls in one process, while permitting distinct lightweight view wrappers.
	firstAST, err := contractcheck.LoadGoASTCorpus(corpus.Path("lycaon/test/contract/sample"))
	contractcheck.FailErr(t, "first LoadGoASTCorpus", err)
	secondAST, err := contractcheck.LoadGoASTCorpus(corpus.Path("lycaon/test/contract/sample"))
	contractcheck.FailErr(t, "second LoadGoASTCorpus", err)
	firstFiles, secondFiles := firstAST.Files(), secondAST.Files()
	if firstAST.Fset == nil || firstAST.Fset != secondAST.Fset {
		t.Fatalf("expected shared token.FileSet across repeated LoadGoASTCorpus calls")
	}
	if len(firstFiles) != 1 || len(secondFiles) != 1 {
		t.Fatalf("expected 1 file from repeated LoadGoASTCorpus calls: first=%d second=%d", len(firstFiles), len(secondFiles))
	}
	if firstFiles[0].Rel != secondFiles[0].Rel || firstFiles[0].AST != secondFiles[0].AST {
		t.Fatalf("expected reused parsed AST and path across repeated LoadGoASTCorpus calls: first=%s second=%s sameAST=%v",
			firstFiles[0].Rel, secondFiles[0].Rel, firstFiles[0].AST == secondFiles[0].AST)
	}
	findGC := func(g *testcorpus.GoCorpus) []string {
		var locs []string
		for _, f := range g.Files() {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "GC" {
					pos := g.Fset.Position(sel.Pos())
					locs = append(locs, fmt.Sprintf("%s:%d", f.Rel, pos.Line))
				}
				return true
			})
		}
		return locs
	}
	firstLocs, secondLocs := findGC(firstAST), findGC(secondAST)
	if len(firstLocs) != 1 || !slices.Equal(firstLocs, secondLocs) {
		t.Fatalf("expected identical seeded GC findings across repeated LoadGoASTCorpus calls: first=%v second=%v", firstLocs, secondLocs)
	}
}

type normalizedFinding struct {
	ruleID   string
	location string
}

func normalizeFindings(familyName string, r invariants.Invariant, res invariants.Result) []normalizedFinding {
	out := make([]normalizedFinding, 0, len(res.Violations))
	ruleID := familyName + "/" + r.Name
	for _, v := range res.Violations {
		loc, _, _ := strings.Cut(v, ": ")
		out = append(out, normalizedFinding{ruleID: ruleID, location: loc})
	}
	return out
}

// Behavioral contract 2: optimized source selection preserves direct and
// multi-package indirect violations, keeps malformed unrelated siblings from
// breaking a scoped scan, and still surfaces parse errors inside the requested
// scope.
func TestOptimizedSourceSelectionPreservesFindingsAndScope(t *testing.T) {
	t.Parallel()
	// Typed rule resolving *pongo2.Template.Execute across package boundaries
	// even when the calling package does not directly import pongo2.
	typedCeilingRule := invariants.Invariant{
		Name:    "typed-template-execute-ceiling",
		Anchors: []invariants.Anchor{invariants.Func("internal/pongoplain", "Compile")},
		Check: func(r *invariants.Run) {
			for _, pkg := range r.TypedPackages("internal/target") {
				for _, f := range pkg.Files {
					ast.Inspect(f.AST, func(n ast.Node) bool {
						call, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}
						if fn := pkg.Callee(call); fn != nil && fn.Pkg() != nil &&
							fn.Pkg().Path() == "github.com/flosch/pongo2/v6" && fn.Name() == "Execute" {
							r.ReportAt(call.Pos(), "direct pongo2.Template.Execute bypasses pongoplain.Execute")
						}
						return true
					})
				}
			}
		},
	}

	// 1. Direct declaration fixture.
	directFiles := with(pongoStub, map[string]string{
		"lycaon/internal/target/direct.go": "package target\n\nimport \"github.com/lycaon/lycaon/internal/pongoplain\"\n\n" +
			"func run() {\n\ttpl, _ := pongoplain.Compile(\"hello\")\n\t_, _ = tpl.Execute(nil)\n}\n",
	})
	directRes := invariants.Evaluate(fixture(t, directFiles), typedCeilingRule)
	expect(t, directRes, violates, 1)
	if got := normalizeFindings("typed", typedCeilingRule, directRes); !slices.Equal(got, []normalizedFinding{
		{ruleID: "typed/typed-template-execute-ceiling", location: "internal/target/direct.go:7"},
	}) {
		t.Fatalf("direct findings = %+v", got)
	}

	// 2. Multi-package helper chain fixture: leaf compiles, mid forwards, target executes directly
	// without directly importing pongo2.
	chainFiles := with(pongoStub, map[string]string{
		"lycaon/internal/leaf/leaf.go": "package leaf\n\nimport (\n\t\"github.com/flosch/pongo2/v6\"\n\t\"github.com/lycaon/lycaon/internal/pongoplain\"\n)\n\n" +
			"func Build() *pongo2.Template {\n\ttpl, _ := pongoplain.Compile(\"hello\")\n\treturn tpl\n}\n",
		"lycaon/internal/mid/bridge.go": "package mid\n\nimport (\n\t\"github.com/flosch/pongo2/v6\"\n\t\"github.com/lycaon/lycaon/internal/leaf\"\n)\n\n" +
			"func Forward() *pongo2.Template { return leaf.Build() }\n",
		"lycaon/internal/target/call.go": "package target\n\nimport \"github.com/lycaon/lycaon/internal/mid\"\n\n" +
			"func invoke() {\n\ttpl := mid.Forward()\n\t_, _ = tpl.Execute(nil)\n}\n",
	})
	chainRes := invariants.Evaluate(fixture(t, chainFiles), typedCeilingRule)
	expect(t, chainRes, violates, 1)
	if got := normalizeFindings("typed", typedCeilingRule, chainRes); !slices.Equal(got, []normalizedFinding{
		{ruleID: "typed/typed-template-execute-ceiling", location: "internal/target/call.go:7"},
	}) {
		t.Fatalf("multi-package chain findings = %+v", got)
	}

	// 3. Valid requested subtree with a malformed sibling vs error inside scope.
	scopedRule := invariants.Invariant{
		Name:    "scoped-subtree-forced-gc",
		Anchors: []invariants.Anchor{invariants.Dir("lycaon/internal/target")},
		Check: func(r *invariants.Run) {
			astCorpus, err := contractcheck.LoadGoASTCorpus(r.Path("lycaon/internal/target"))
			r.Must(err)
			for _, f := range astCorpus.Files() {
				ast.Inspect(f.AST, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "runtime" && sel.Sel.Name == "GC" {
						pos := astCorpus.Fset.Position(sel.Pos())
						rel, err := filepath.Rel(r.Path("lycaon"), pos.Filename)
						r.Must(err)
						r.Report("%s:%d: explicit runtime.GC in scoped subtree", filepath.ToSlash(rel), pos.Line)
					}
					return true
				})
			}
		},
	}

	validWithMalformedSibling := map[string]string{
		"lycaon/internal/target/direct.go":  "package target\n\nimport \"runtime\"\n\nfunc run() { runtime.GC() }\n",
		"lycaon/internal/sibling/broken.go": "package sibling\n\nfunc (\n",
	}
	scopedRes := invariants.Evaluate(fixture(t, validWithMalformedSibling), scopedRule)
	expect(t, scopedRes, violates, 1)
	if got := normalizeFindings("scoped", scopedRule, scopedRes); !slices.Equal(got, []normalizedFinding{
		{ruleID: "scoped/scoped-subtree-forced-gc", location: "internal/target/direct.go:5"},
	}) {
		t.Fatalf("scoped subtree findings with malformed sibling = %+v", got)
	}

	malformedInsideScope := with(validWithMalformedSibling, map[string]string{
		"lycaon/internal/target/broken.go": "package target\n\nfunc (\n",
	})
	expect(t, invariants.Evaluate(fixture(t, malformedInsideScope), scopedRule), fails, 0)
}
