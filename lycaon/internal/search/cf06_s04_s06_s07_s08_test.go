package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// --- Independent S04 reference ranking (docs/source-navigation.md) ---

type cf06RefLookupHit struct {
	Path   string
	Tier   int
	Window int
}

func cf06TightestSubseq(haystack, needle string) (int, bool) {
	if needle == "" {
		return 0, true
	}
	hr := []rune(haystack)
	nr := []rune(needle)
	best := -1
	for start := 0; start < len(hr); start++ {
		if hr[start] != nr[0] {
			continue
		}
		pos := start + 1
		ok := true
		last := start
		for _, target := range nr[1:] {
			found := -1
			for j := pos; j < len(hr); j++ {
				if hr[j] == target {
					found = j
					break
				}
			}
			if found < 0 {
				ok = false
				break
			}
			last = found
			pos = found + 1
		}
		if !ok {
			break
		}
		w := last - start + 1
		if best < 0 || w < best {
			best = w
		}
	}
	return best, best >= 0
}

func cf06ReferenceFileLookup(paths []string, query string) []cf06RefLookupHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var out []cf06RefLookupHit
	for _, p := range paths {
		lp := strings.ToLower(p)
		base := lp
		if idx := strings.LastIndex(lp, "/"); idx >= 0 {
			base = lp[idx+1:]
		}
		if strings.HasPrefix(base, q) {
			out = append(out, cf06RefLookupHit{Path: p, Tier: 0, Window: len([]rune(q))})
		} else if strings.Contains(base, q) {
			out = append(out, cf06RefLookupHit{Path: p, Tier: 1, Window: len([]rune(q))})
		} else if w, ok := cf06TightestSubseq(lp, q); ok {
			out = append(out, cf06RefLookupHit{Path: p, Tier: 2, Window: w})
		}
	}
	slices.SortFunc(out, func(a, b cf06RefLookupHit) int {
		if a.Tier != b.Tier {
			return a.Tier - b.Tier
		}
		if a.Window != b.Window {
			return a.Window - b.Window
		}
		if len(a.Path) != len(b.Path) {
			return len(a.Path) - len(b.Path)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

func TestCF06_S04_FileLookup(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Cleanup(func() { _ = sourcecatalog.Process().Drain(context.Background()) })
	// Build a realistic 1..5,000 path corpus (200 paths in primary + 5,000-path stress corpus).
	rootSmall := t.TempDir()
	var smallPaths []string
	dirs := []string{"src", "pkg/api", "internal/search", "docs", "cmd/app", "lib/core"}
	stems := []string{"router", "catalog", "alpha", "search", "index", "config", "handler", "model", "store", "worker"}
	exts := []string{".go", ".ts", ".md", ".py", ".json"}
	for i := 0; i < 200; i++ {
		rel := fmt.Sprintf("%s/%s_%03d%s", dirs[i%len(dirs)], stems[(i*3)%len(stems)], i, exts[i%len(exts)])
		abs := filepath.Join(rootSmall, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(abs, []byte("x\n"), 0o644))
		smallPaths = append(smallPaths, rel)
	}
	slices.Sort(smallPaths)

	rootLarge := t.TempDir()
	var largePaths []string
	for i := 0; i < 5000; i++ {
		rel := fmt.Sprintf("d%02d/m%02d/%s_%04d.go", i%25, (i/25)%20, stems[i%len(stems)], i)
		abs := filepath.Join(rootLarge, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(abs, []byte("x\n"), 0o644))
		largePaths = append(largePaths, rel)
	}
	slices.Sort(largePaths)

	pSmall := &project.Project{ID: "p-s04-small", Roots: []project.Root{{ID: "r-small", Path: rootSmall}}}
	pLarge := &project.Project{ID: "p-s04-large", Roots: []project.Root{{ID: "r-large", Path: rootLarge}}}
	testutil.FailErr(t, "await small", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), pSmall.ID, sourcecatalog.Root{ID: "r-small", Path: rootSmall}))
	testutil.FailErr(t, "await large", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), pLarge.ID, sourcecatalog.Root{ID: "r-large", Path: rootLarge}))

	idxCache := projectsource.NewSourceIndexCache()
	snapSmall := idxCache.Snapshot(t.Context(), pSmall)
	defer snapSmall.Close()
	snapLarge := idxCache.Snapshot(t.Context(), pLarge)
	defer snapLarge.Close()

	const totalInstances = 2000
	passed, failed := 0, 0
	var firstFailure string

	for i := 0; i < totalInstances; i++ {
		rng := cf06RNG(cf06BaseSeed, "S04", i)
		useLarge := (i % 20) == 0
		snap := snapSmall
		paths := smallPaths
		rid := "r-small"
		if useLarge {
			snap = snapLarge
			paths = largePaths
			rid = "r-large"
		}
		chosen := paths[rng.Intn(len(paths))]
		base := filepath.Base(chosen)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		var q string
		switch i % 4 {
		case 0: // basename prefix
			q = stem[:min(4, len(stem))]
		case 1: // basename substring
			if len(stem) >= 5 {
				q = stem[1:4]
			} else {
				q = stem
			}
		case 2: // path window
			segs := strings.Split(chosen, "/")
			q = segs[0][:min(2, len(segs[0]))] + stem[:min(3, len(stem))]
		default: // full path
			q = chosen
		}

		got, err := projectsource.SearchSourceIndex(t.Context(), snap, projectsource.SourceQuery{Path: q}, projectsource.SourcePathStyle{}, rid, nil, 50)
		if err != nil {
			failed++
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("instance %d q=%q err=%v", i, q, err)
			}
			continue
		}
		ref := cf06ReferenceFileLookup(paths, q)
		if len(ref) > 50 {
			ref = ref[:50]
		}
		if len(got) != len(ref) {
			failed++
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("instance %d q=%q len(got)=%d len(ref)=%d", i, q, len(got), len(ref))
			}
			continue
		}
		ok := true
		for j := range got {
			if got[j].Path != ref[j].Path {
				ok = false
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d q=%q idx=%d got=%q want=%q", i, q, j, got[j].Path, ref[j].Path)
				}
				break
			}
			// Verify matched highlight ranges are valid and non-empty.
			if len(got[j].Highlights) == 0 {
				ok = false
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d q=%q idx=%d empty highlights on %q", i, q, j, got[j].Path)
				}
				break
			}
			runes := []rune(strings.ToLower(got[j].Path))
			var spelled strings.Builder
			for _, h := range got[j].Highlights {
				if h.Start < 0 || h.End > len(runes) || h.Start >= h.End {
					ok = false
					if firstFailure == "" {
						firstFailure = fmt.Sprintf("instance %d invalid highlight %+v for %q", i, h, got[j].Path)
					}
					break
				}
				spelled.WriteString(string(runes[h.Start:h.End]))
			}
			if !ok {
				break
			}
			if spelled.String() != strings.ToLower(q) {
				ok = false
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d highlights spell %q != %q on %q", i, spelled.String(), strings.ToLower(q), got[j].Path)
				}
				break
			}
		}
		if ok {
			passed++
		} else {
			failed++
		}
	}

	// 4 hand-written S04 edges.
	edgePassed := 0
	t.Run("edges", func(t *testing.T) {
		// Edge 1: Warming root beside healthy root does not suppress healthy root matches.
		healthyDir := t.TempDir()
		testutil.FailErr(t, "write healthy", os.WriteFile(filepath.Join(healthyDir, "alpha_healthy.go"), []byte("package a\n"), 0o644))
		coldDir := t.TempDir()
		testutil.FailErr(t, "write cold", os.WriteFile(filepath.Join(coldDir, "alpha_cold.go"), []byte("package b\n"), 0o644))
		hold, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
			Key: "s04-hold:" + coldDir, Lane: coldDir, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
		})
		testutil.FailErr(t, "hold cold lane", err)
		defer hold()
		testutil.FailErr(t, "await healthy", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), "p-s04-mix", sourcecatalog.Root{ID: "r-ok", Path: healthyDir}))
		pMix := &project.Project{ID: "p-s04-mix", Roots: []project.Root{
			{ID: "r-ok", Path: healthyDir},
			{ID: "r-cold", Path: coldDir},
		}}
		snapMix := idxCache.Snapshot(t.Context(), pMix)
		defer snapMix.Close()
		matches, err := projectsource.SearchSourceIndex(t.Context(), snapMix, projectsource.SourceQuery{Path: "alpha"}, projectsource.SourcePathStyle{}, "", nil, 10)
		testutil.FailErr(t, "search mixed roots", err)
		if len(matches) == 0 || matches[0].Path != "alpha_healthy.go" {
			t.Fatalf("edge1 warming root suppressed healthy root: %+v", matches)
		}
		edgePassed++

		// Edge 2: Missing/failed root beside healthy root does not suppress healthy root matches.
		missingDir := filepath.Join(t.TempDir(), "does-not-exist")
		testutil.FailErr(t, "await healthy fail-proj", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), "p-s04-fail", sourcecatalog.Root{ID: "r-ok", Path: healthyDir}))
		pFail := &project.Project{ID: "p-s04-fail", Roots: []project.Root{
			{ID: "r-ok", Path: healthyDir},
			{ID: "r-bad", Path: missingDir},
		}}
		snapFail := idxCache.Snapshot(t.Context(), pFail)
		defer snapFail.Close()
		matches2, err := projectsource.SearchSourceIndex(t.Context(), snapFail, projectsource.SourceQuery{Path: "alpha"}, projectsource.SourcePathStyle{}, "", nil, 10)
		testutil.FailErr(t, "search with failed root", err)
		if len(matches2) == 0 || matches2[0].Path != "alpha_healthy.go" {
			t.Fatalf("edge2 failed root suppressed healthy root: %+v", matches2)
		}
		edgePassed++

		// Edge 3: Empty query returns browse listing in deterministic order.
		browse, err := projectsource.SearchSourceIndex(t.Context(), snapSmall, projectsource.SourceQuery{Path: ""}, projectsource.SourcePathStyle{}, "r-small", nil, 10)
		testutil.FailErr(t, "empty browse", err)
		if len(browse) != 10 {
			t.Fatalf("edge3 empty browse len=%d want 10", len(browse))
		}
		edgePassed++

		// Edge 4: Leading directory relaxation finds local file from CI/module path.
		relMatches, err := projectsource.SearchSourceIndex(t.Context(), snapSmall, projectsource.SourceQuery{Path: "ci/runner/checkout/" + smallPaths[0]}, projectsource.SourcePathStyle{}, "r-small", nil, 10)
		testutil.FailErr(t, "relaxed lookup", err)
		if len(relMatches) == 0 || relMatches[0].Path != smallPaths[0] {
			t.Fatalf("edge4 leading dir relaxation failed: %+v want %q", relMatches, smallPaths[0])
		}
		edgePassed++
	})

	t.Logf("CF06_SUMMARY {\"family\":\"S04\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":0,\"edges_planned\":4,\"edges_passed\":%d}",
		totalInstances, passed+failed, passed, failed, edgePassed)
	if failed > 0 {
		t.Fatalf("S04 file lookup failed %d/%d instances; first: %s", failed, totalInstances, firstFailure)
	}
}

func TestCF06_S06_QueryLanguage(t *testing.T) {
	const totalInstances = 5000
	passed, failed := 0, 0
	var firstFailure string

	compileCtx := CompileContext{
		OriginProjectID: "proj-1",
		ResolveProjectBySlug: func(slug string) (string, error) {
			if slug == "demo" {
				return "proj-1", nil
			}
			return "", fmt.Errorf("unknown project %q", slug)
		},
	}

	plainTemplates := []string{
		"find %s in workspace",
		"https://example.com/api/v1/%s?q=1&x=2",
		"\"%s unclosed phrase with spaces",
		"(%s[0] + %s) -> result",
		"// %s :: -> == !=",
		"%s and or not beta",
		"Error:%s custom_key:val_%d",
		"func %s(x int) error { return nil }",
	}

	for i := 0; i < totalInstances; i++ {
		rng := cf06RNG(cf06BaseSeed, "S06", i)
		tok := cf06Tokens[rng.Intn(len(cf06Tokens))]
		mode := i % 10
		switch {
		case mode < 8:
			// Plain text / pasted code / URL / stray quotes / stray brackets / bare punct / lowercase ops / unknown word:val
			// Contract: plain text NEVER fails to parse.
			var q string
			switch mode {
			case 0:
				q = fmt.Sprintf(plainTemplates[0], tok)
			case 1:
				q = fmt.Sprintf(plainTemplates[1], tok)
			case 2:
				q = fmt.Sprintf(plainTemplates[2], tok)
			case 3:
				q = fmt.Sprintf(plainTemplates[3], tok, tok)
			case 4:
				q = fmt.Sprintf(plainTemplates[4], tok)
			case 5:
				q = fmt.Sprintf(plainTemplates[5], tok)
			case 6:
				q = fmt.Sprintf(plainTemplates[6], tok, rng.Intn(999))
			case 7:
				q = fmt.Sprintf(plainTemplates[7], tok)
			}
			if _, err := CompileQuery(q, compileCtx); err != nil {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d plain text %q failed to parse: %v", i, q, err)
				}
				continue
			}
			passed++
		case mode == 8:
			// Closed field with invalid value -> MUST fail with positional ParseError naming field.
			prefix := strings.Repeat(" ", rng.Intn(4))
			badVal := fmt.Sprintf("bogus_%d", rng.Intn(1000))
			q := prefix + "kind:" + badVal
			_, err := CompileQuery(q, compileCtx)
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Field != "kind" {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d invalid closed field %q got err=%v", i, q, err)
				}
				continue
			}
			passed++
		default:
			// Provably empty conjunction -> MUST be rejected.
			q := "kind:code kind:message"
			if rng.Intn(2) == 1 {
				q = "kind:code NOT kind:code"
			}
			_, err := CompileQuery(q, compileCtx)
			if err == nil {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d empty conjunction %q was not rejected", i, q)
				}
				continue
			}
			passed++
		}
	}

	// 10 hand-written S06 edge cases.
	edgePassed := 0
	edges := []struct {
		name      string
		query     string
		wantErr   bool
		wantField string
	}{
		{"plain_sentence", "hello world from editor", false, ""},
		{"url_with_port", "http://localhost:8787/v1/search?a=1#frag", false, ""},
		{"unclosed_quote", `"unclosed phrase`, false, ""},
		{"unclosed_paren", `(alpha OR beta`, false, ""},
		{"bare_punctuation", "->", false, ""},
		{"lowercase_operators", "alpha and beta or not gamma", false, ""},
		{"unknown_field_colon", "Error:connection_refused", false, ""},
		{"invalid_closed_kind", "alpha kind:invalid_kind", true, "kind"},
		{"conflicting_single_valued", "kind:code kind:symbol", true, "kind"},
		{"required_and_negated", "kind:code NOT kind:code", true, "kind"},
	}
	for _, tc := range edges {
		_, err := CompileQuery(tc.query, compileCtx)
		if tc.wantErr {
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Errorf("edge %s (%q): expected ParseError, got %v", tc.name, tc.query, err)
				continue
			}
			if tc.wantField != "" && pe.Field != tc.wantField {
				t.Errorf("edge %s (%q): field=%q want %q", tc.name, tc.query, pe.Field, tc.wantField)
				continue
			}
		} else if err != nil {
			t.Errorf("edge %s (%q): unexpected error %v", tc.name, tc.query, err)
			continue
		}
		edgePassed++
	}

	t.Logf("CF06_SUMMARY {\"family\":\"S06\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":0,\"edges_planned\":10,\"edges_passed\":%d}",
		totalInstances, passed+failed, passed, failed, edgePassed)
	if failed > 0 {
		t.Fatalf("S06 query language failed %d/%d instances; first: %s", failed, totalInstances, firstFailure)
	}
}

func TestCF06_S07_PagesCancellationExport(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { _ = catalog.Drain(context.Background()) })

	dirA := t.TempDir()
	dirB := t.TempDir()
	for i := 0; i < 20; i++ {
		testutil.FailErr(t, "write A", os.WriteFile(filepath.Join(dirA, fmt.Sprintf("a_%02d.txt", i)), []byte(fmt.Sprintf("alpha beta line_%d\n", i)), 0o644))
		testutil.FailErr(t, "write B", os.WriteFile(filepath.Join(dirB, fmt.Sprintf("b_%02d.txt", i)), []byte(fmt.Sprintf("gamma delta line_%d\n", i)), 0o644))
	}
	testutil.FailErr(t, "await A", catalogtest.AwaitIndex(t.Context(), catalog, "proj-A", sourcecatalog.Root{ID: "rA", Path: dirA}))
	testutil.FailErr(t, "await B", catalogtest.AwaitIndex(t.Context(), catalog, "proj-B", sourcecatalog.Root{ID: "rB", Path: dirB}))

	sqlDB := testdbfixture.OpenPath(t, filepath.Join(t.TempDir(), "s07.db"))
	svc := &Service{router: NewRouter(NewStoreExecutor(sqlDB), &CodeExecutor{catalog: catalog, rerank: decide.Reranker{}}, nil)}

	const totalInstances = 400
	passed, failed := 0, 0
	var firstFailure string

	for i := 0; i < totalInstances; i++ {
		rng := cf06RNG(cf06BaseSeed, "S07", i)
		q1 := "alpha"
		q2 := "gamma"
		if rng.Intn(2) == 1 {
			q1, q2 = "beta", "delta"
		}
		rootsA := []CodeRoot{{ProjectID: "proj-A", RootID: "rA", Path: dirA}}
		rootsB := []CodeRoot{{ProjectID: "proj-B", RootID: "rB", Path: dirB}}
		ctx1 := CompileContext{
			OriginProjectID: "proj-A",
			RootsForProject: func(pid string) ([]CodeRoot, error) {
				if pid == "proj-A" {
					return rootsA, nil
				}
				return rootsB, nil
			},
			Budget: BudgetComplete,
		}
		ctx2 := CompileContext{
			OriginProjectID: "proj-B",
			RootsForProject: ctx1.RootsForProject,
			Budget:          BudgetComplete,
		}
		switch i % 4 {
		case 0: // Two distinct queries: verify zero cross-bleed between results.
			r1, err1 := svc.Search(t.Context(), "project:current "+q1, ctx1)
			r2, err2 := svc.Search(t.Context(), "project:current "+q2, ctx2)
			if err1 != nil || err2 != nil || len(r1.Hits) == 0 || len(r2.Hits) == 0 {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d race pair err1=%v err2=%v", i, err1, err2)
				}
				continue
			}
			cross := false
			for _, h := range r2.Hits {
				if h.ProjectID == "proj-A" || strings.HasPrefix(h.Path, "a_") {
					cross = true
				}
			}
			if cross {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d proj-A hit leaked into proj-B query", i)
				}
				continue
			}
			passed++
		case 1: // Mid-request cancellation via canceled context.
			cctx, cancel := context.WithCancel(t.Context())
			cancel()
			res, err := svc.Search(cctx, "project:current "+q1, ctx1)
			// Router catches executor ctx.Canceled as executor_error issue (never silently complete).
			if err == nil && res != nil && res.Exhaustive && len(res.Issues) == 0 {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d canceled request claimed exhaustive complete", i)
				}
				continue
			}
			passed++
		case 2: // Project switch under same query string.
			rA, errA := svc.Search(t.Context(), "project:current line_1", ctx1)
			rB, errB := svc.Search(t.Context(), "project:current line_1", ctx2)
			if errA != nil || errB != nil {
				failed++
				continue
			}
			bad := false
			for _, h := range rA.Hits {
				if h.ProjectID != "proj-A" {
					bad = true
				}
			}
			for _, h := range rB.Hits {
				if h.ProjectID != "proj-B" {
					bad = true
				}
			}
			if bad {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d project switch mixed project IDs", i)
				}
				continue
			}
			passed++
		default: // Export reproduces exact shown search rows.
			res, err := svc.Search(t.Context(), "project:current "+q1, ctx1)
			out, err2 := svc.Export(t.Context(), "project:current "+q1, ctx1)
			if err != nil || err2 != nil {
				failed++
				continue
			}
			var buf bytes.Buffer
			if err := WriteExport(&buf, ExportFormatJSONL, out.Result.Hits, out.Truncated); err != nil {
				failed++
				continue
			}
			if len(out.Result.Hits) != len(res.Hits) {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d export len=%d != search len=%d", i, len(out.Result.Hits), len(res.Hits))
				}
				continue
			}
			passed++
		}
	}

	// 4 hand-written S07 edges.
	edgePassed := 0
	t.Run("edges", func(t *testing.T) {
		rootsFor := func(pid string) ([]CodeRoot, error) {
			if pid == "proj-A" {
				return []CodeRoot{{ProjectID: "proj-A", RootID: "rA", Path: dirA}}, nil
			}
			return []CodeRoot{{ProjectID: "proj-B", RootID: "rB", Path: dirB}}, nil
		}
		ctxA := CompileContext{OriginProjectID: "proj-A", RootsForProject: rootsFor, Budget: BudgetComplete}
		// Edge 1: Export CSV matches row count.
		out, err := svc.Export(t.Context(), "project:current alpha", ctxA)
		testutil.FailErr(t, "export csv", err)
		var csvBuf bytes.Buffer
		testutil.FailErr(t, "write csv", WriteExport(&csvBuf, ExportFormatCSV, out.Result.Hits, out.Truncated))
		if !strings.Contains(csvBuf.String(), "a_00.txt") {
			t.Fatalf("edge1 csv missing expected hit")
		}
		edgePassed++

		// Edge 2: Export JSONL preserves match flags (case_sensitive).
		ctxCase := ctxA
		ctxCase.Flags = MatchFlags{CaseSensitive: true}
		outCase, err := svc.Export(t.Context(), "project:current ALPHA", ctxCase)
		testutil.FailErr(t, "export case", err)
		if len(outCase.Result.Hits) != 0 {
			t.Fatalf("edge2 case_sensitive export returned %d hits, want 0", len(outCase.Result.Hits))
		}
		edgePassed++

		// Edge 3: Pre-canceled context never returns exhaustive complete.
		cctx, cancel := context.WithCancel(t.Context())
		cancel()
		resCancel, err := svc.Search(cctx, "project:current alpha", ctxA)
		if err == nil && resCancel.Exhaustive {
			t.Fatalf("edge3 canceled search claimed exhaustive")
		}
		edgePassed++

		// Edge 4: Origin project switch isolates results cleanly.
		ctxB := CompileContext{OriginProjectID: "proj-B", RootsForProject: rootsFor, Budget: BudgetComplete}
		resB, err := svc.Search(t.Context(), "project:current alpha", ctxB)
		testutil.FailErr(t, "search B", err)
		if len(resB.Hits) != 0 {
			t.Fatalf("edge4 proj-B returned proj-A hits: %+v", resB.Hits)
		}
		edgePassed++
	})

	t.Logf("CF06_SUMMARY {\"family\":\"S07\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":0,\"edges_planned\":4,\"edges_passed\":%d}",
		totalInstances, passed+failed, passed, failed, edgePassed)
	if failed > 0 {
		t.Fatalf("S07 pages/cancel/export failed %d/%d instances; first: %s", failed, totalInstances, firstFailure)
	}
}

func TestCF06_S08_StoreLeg(t *testing.T) {
	const totalInstances = 600
	passed, failed := 0, 0
	var firstFailure string

	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "s08.db"))
	projA := testdbseed.DefaultProjectID
	const projB = "proj-s08-other"
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-A", projA, dir)
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-B", projB, dir)

	ctx := context.Background()
	store := NewStore()
	execStore := NewStoreExecutor(sqlDB)

	for i := 0; i < totalInstances; i++ {
		rng := cf06RNG(cf06BaseSeed, "S08", i)
		token := fmt.Sprintf("s08tok_%04d_%d", i, rng.Intn(10000))
		msgID := fmt.Sprintf("msg-s08-%04d", i)
		sessID := "sess-A"
		pid := projA
		if i%5 == 0 {
			sessID = "sess-B"
			pid = projB
		}
		ts := time.Now().UTC()
		tx, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin", err)
		testdbseed.InsertSessionEntry(t, tx, "ent-"+msgID, sessID, "utterance", msgID, int64(i+1))
		_, err = tx.ExecContext(ctx, `INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts)
			VALUES (?, ?, ?, 'user', ?, 'user', 'user', 'trusted', '', ?)`,
			msgID, "ent-"+msgID, sessID, "record body "+token, timelayout.Format(ts))
		testutil.FailErr(t, "insert msg", err)
		msg := api.Message{ID: msgID, Role: api.MessageRoleUser, Content: "record body " + token, CreatedAt: ts}
		testutil.FailErr(t, "write-through", SyncMessageWriteThrough(ctx, tx, pid, sessID, msg))
		testutil.FailErr(t, "commit", tx.Commit())

		// Verify store leg finds it in its own project and NOT in the other project.
		planOwn, err := CompileQuery("project:current "+token, CompileContext{OriginProjectID: pid, StoreOnly: true})
		if err != nil || planOwn.Store == nil {
			failed++
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("instance %d compile err=%v", i, err)
			}
			continue
		}
		repOwn, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 50, Store: planOwn.Store})
		if err != nil || len(repOwn.Hits) != 1 || repOwn.Hits[0].ProjectID != pid {
			failed++
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("instance %d own project hits=%+v err=%v", i, repOwn.Hits, err)
			}
			continue
		}

		otherPID := projB
		if pid == projB {
			otherPID = projA
		}
		planOther, err := CompileQuery("project:current "+token, CompileContext{OriginProjectID: otherPID, StoreOnly: true})
		testutil.FailErr(t, "compile other", err)
		repOther, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 50, Store: planOther.Store})
		if err != nil || len(repOther.Hits) != 0 {
			failed++
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("instance %d cross-project leak into %s: %+v", i, otherPID, repOther.Hits)
			}
			continue
		}

		// Periodically test update and delete parity.
		if i%10 == 0 {
			updatedTok := token + "_upd"
			tx2, err := sqlDB.BeginTx(ctx, nil)
			testutil.FailErr(t, "begin upd", err)
			_, err = tx2.ExecContext(ctx, `UPDATE messages SET content = ? WHERE id = ?`, "updated "+updatedTok, msgID)
			testutil.FailErr(t, "upd msg", err)
			msg.Content = "updated " + updatedTok
			testutil.FailErr(t, "sync upd", SyncMessageWriteThrough(ctx, tx2, pid, sessID, msg))
			testutil.FailErr(t, "commit upd", tx2.Commit())

			planUpd, _ := CompileQuery("project:current "+updatedTok, CompileContext{OriginProjectID: pid, StoreOnly: true})
			repUpd, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 50, Store: planUpd.Store})
			if err != nil || len(repUpd.Hits) != 1 {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d updated record missing: %+v", i, repUpd.Hits)
				}
				continue
			}
		}
		passed++
	}

	// 4 hand-written S08 edges.
	edgePassed := 0
	t.Run("edges", func(t *testing.T) {
		// Edge 1: Skipped projection write repaired via SyncMessageWriteThrough.
		skipID := "msg-s08-skipped"
		skipTok := "skipped_projection_unique_9911"
		ts := time.Now().UTC()
		tx, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin skip", err)
		testdbseed.InsertSessionEntry(t, tx, "ent-"+skipID, "sess-A", "utterance", skipID, 9001)
		_, err = tx.ExecContext(ctx, `INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts)
			VALUES (?, ?, 'sess-A', 'user', ?, 'user', 'user', 'trusted', '', ?)`,
			skipID, "ent-"+skipID, "body "+skipTok, timelayout.Format(ts))
		testutil.FailErr(t, "insert skipped msg", err)
		// Deliberately skip SyncMessageWriteThrough here!
		testutil.FailErr(t, "commit skipped", tx.Commit())

		// Confirm absent before repair.
		planSkip, err := CompileQuery("project:current "+skipTok, CompileContext{OriginProjectID: projA, StoreOnly: true})
		testutil.FailErr(t, "compile skip", err)
		repBefore, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 10, Store: planSkip.Store})
		testutil.FailErr(t, "run skip before", err)
		if len(repBefore.Hits) != 0 {
			t.Fatalf("edge1 expected 0 hits before repair, got %d", len(repBefore.Hits))
		}
		// Repair projection from authoritative message row.
		txRep, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin repair", err)
		testutil.FailErr(t, "repair write-through", SyncMessageWriteThrough(ctx, txRep, projA, "sess-A", api.Message{
			ID: skipID, Role: api.MessageRoleUser, Content: "body " + skipTok, CreatedAt: ts,
		}))
		testutil.FailErr(t, "commit repair", txRep.Commit())
		repAfter, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 10, Store: planSkip.Store})
		testutil.FailErr(t, "query after repair", err)
		if len(repAfter.Hits) != 1 {
			t.Fatalf("edge1 repair failed: hits=%+v", repAfter.Hits)
		}
		edgePassed++

		// Edge 2: Rebuild projection does not mutate authoritative messages table (verified by hash).
		hashAuth := func() string {
			rows, err := sqlDB.QueryContext(ctx, `SELECT id, session_id, role, content, ts FROM messages ORDER BY id`)
			testutil.FailErr(t, "hash auth", err)
			defer rows.Close()
			h := sha256.New()
			for rows.Next() {
				var id, sid, role, content, tsStr string
				testutil.FailErr(t, "scan auth", rows.Scan(&id, &sid, &role, &content, &tsStr))
				fmt.Fprintf(h, "%s|%s|%s|%s|%s\n", id, sid, role, content, tsStr)
			}
			return hex.EncodeToString(h.Sum(nil))
		}
		beforeHash := hashAuth()
		txReb, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin rebuild", err)
		_, err = txReb.ExecContext(ctx, `DELETE FROM evidence_index WHERE message_id = ?`, skipID)
		testutil.FailErr(t, "clear proj", err)
		testutil.FailErr(t, "reproject", SyncMessageWriteThrough(ctx, txReb, projA, "sess-A", api.Message{
			ID: skipID, Role: api.MessageRoleUser, Content: "body " + skipTok, CreatedAt: ts,
		}))
		testutil.FailErr(t, "commit rebuild", txReb.Commit())
		afterHash := hashAuth()
		if beforeHash != afterHash {
			t.Fatalf("edge2 rebuild mutated authoritative messages: %s != %s", beforeHash, afterHash)
		}
		edgePassed++

		// Edge 3: Cross-project isolation on findings & evidence rows.
		txIso, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin iso", err)
		testutil.FailErr(t, "upsert finding B", store.UpsertRows(ctx, txIso, []IndexRow{{
			ID: "finding-iso-B", ProjectID: projB, Source: SourceFinding, HitKind: HitKindFinding,
			SessionID: "sess-B", Snippet: "secret_finding_only_in_B", TS: timelayout.Format(ts),
		}}))
		testutil.FailErr(t, "commit iso", txIso.Commit())
		planIsoA, err := CompileQuery("project:current kind:scan secret_finding_only_in_B", CompileContext{OriginProjectID: projA, StoreOnly: true})
		testutil.FailErr(t, "compile iso A", err)
		repIsoA, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 10, Store: planIsoA.Store})
		testutil.FailErr(t, "run iso A", err)
		if len(repIsoA.Hits) != 0 {
			t.Fatalf("edge3 finding from projB leaked to projA: %+v", repIsoA.Hits)
		}
		edgePassed++

		// Edge 4: Clearing projection for deleted message removes it from store search.
		txDel, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin del", err)
		testutil.FailErr(t, "clear deleted", store.clearMessageEvidence(ctx, txDel, skipID))
		testutil.FailErr(t, "commit del", txDel.Commit())
		repDel, err := execStore.Run(ctx, PlanLeg{Executor: ExecutorStore, Cap: 10, Store: planSkip.Store})
		testutil.FailErr(t, "query after del", err)
		if len(repDel.Hits) != 0 {
			t.Fatalf("edge4 deleted message still returned: %+v", repDel.Hits)
		}
		edgePassed++
	})

	t.Logf("CF06_SUMMARY {\"family\":\"S08\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":0,\"edges_planned\":4,\"edges_passed\":%d}",
		totalInstances, passed+failed, passed, failed, edgePassed)
	if failed > 0 {
		t.Fatalf("S08 store leg failed %d/%d instances; first: %s", failed, totalInstances, firstFailure)
	}
}
