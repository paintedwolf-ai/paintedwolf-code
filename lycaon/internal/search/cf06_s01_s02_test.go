package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

const cf06BaseSeed = "20261127"

var cf06Tokens = []string{
	"alpha", "beta", "gamma", "delta",
	"catalog", "frontier", "watcher", "snapshot",
	"search_hit", "replace_target", "PaintedWolf", "order_42",
}

func cf06DeriveSeed(baseSeed, familyID string, idx int) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", baseSeed, familyID, idx)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func cf06RNG(baseSeed, familyID string, idx int) *rand.Rand {
	return rand.New(rand.NewSource(cf06DeriveSeed(baseSeed, familyID, idx)))
}

type cf06QueryForm struct {
	Form          string
	Pattern       string
	CaseSensitive bool
	WholeWord     bool
	Regex         bool
	IncludeGlobs  []string
	ExcludeGlobs  []string
	PathFilters   []string
}

type cf06RGLineHit struct {
	Path string
	Line int
}

func cf06RunRipgrep(root string, q cf06QueryForm, includeHidden bool) (map[cf06RGLineHit]int, error) {
	args := []string{"--json", "--no-ignore", "--color", "never", "--sort", "path"}
	if includeHidden {
		args = append(args, "--hidden")
	}
	if !q.Regex {
		args = append(args, "--fixed-strings")
	}
	if q.CaseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	if q.WholeWord {
		args = append(args, "--word-regexp")
	}
	for _, g := range q.IncludeGlobs {
		args = append(args, "--glob", g)
	}
	for _, g := range q.ExcludeGlobs {
		if strings.HasPrefix(g, "!") {
			args = append(args, "--glob", g)
		} else {
			args = append(args, "--glob", "!"+g)
		}
	}
	args = append(args, "--", q.Pattern, ".")
	cmd := exec.Command("rg", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return map[cf06RGLineHit]int{}, nil
		}
		return nil, fmt.Errorf("rg %v: %w", args, err)
	}
	hits := map[cf06RGLineHit]int{}
	for _, rawLine := range bytes.Split(out, []byte{'\n'}) {
		rawLine = bytes.TrimSpace(rawLine)
		if len(rawLine) == 0 {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				LineNumber int `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rawLine, &ev); err != nil || ev.Type != "match" {
			continue
		}
		rel := strings.TrimPrefix(filepath.ToSlash(ev.Data.Path.Text), "./")
		if len(q.PathFilters) > 0 && !cf06MatchesPathFilters(rel, q.PathFilters) {
			continue
		}
		hits[cf06RGLineHit{Path: rel, Line: ev.Data.LineNumber}]++
	}
	return hits, nil
}

func cf06MatchesPathFilters(rel string, filters []string) bool {
	lowered := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	for _, pf := range filters {
		p := strings.ToLower(pf)
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(lowered, p) {
				return true
			}
		} else if lowered == p || strings.HasPrefix(lowered, p+"/") || base == p {
			return true
		}
	}
	return false
}

func cf06MakeCorpus(t *testing.T, baseSeed, familyID string, corpusIdx, fileCount int) (string, []string) {
	t.Helper()
	rng := cf06RNG(baseSeed, familyID+":corpus", corpusIdx)
	root := t.TempDir()
	dirs := []string{"src", "pkg", "lib", "docs", "internal"}
	exts := []string{".txt", ".go", ".py", ".ts", ".md"}
	paths := make([]string, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		dir := dirs[rng.Intn(len(dirs))]
		ext := exts[rng.Intn(len(exts))]
		rel := fmt.Sprintf("%s/f_%04d%s", dir, i, ext)
		lines := rng.Intn(5) + 2
		var sb strings.Builder
		for l := 0; l < lines; l++ {
			t1 := cf06Tokens[rng.Intn(len(cf06Tokens))]
			t2 := cf06Tokens[rng.Intn(len(cf06Tokens))]
			fmt.Fprintf(&sb, "line_%d: %s %s_%d\n", l, t1, t2, rng.Intn(50))
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir corpus dir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write corpus file", os.WriteFile(abs, []byte(sb.String()), 0o644))
		paths = append(paths, rel)
	}
	// Always include one hidden file so default-exclusion vs positive-filter is continuously exercised.
	hiddenPath := filepath.Join(root, ".hidden_cfg.txt")
	testutil.FailErr(t, "write hidden file", os.WriteFile(hiddenPath, []byte("line_0: alpha beta PaintedWolf\n"), 0o644))
	return root, paths
}

func cf06BuildS01Query(rng *rand.Rand, formIdx int, paths []string) cf06QueryForm {
	forms := []string{"literal", "case_sensitive_literal", "whole_word", "regex", "glob_filtered", "path_filtered"}
	form := forms[formIdx%len(forms)]
	tok := cf06Tokens[rng.Intn(len(cf06Tokens))]
	switch form {
	case "literal":
		return cf06QueryForm{Form: form, Pattern: tok}
	case "case_sensitive_literal":
		return cf06QueryForm{Form: form, Pattern: tok, CaseSensitive: true}
	case "whole_word":
		return cf06QueryForm{Form: form, Pattern: tok, WholeWord: true}
	case "regex":
		t2 := cf06Tokens[rng.Intn(len(cf06Tokens))]
		templates := []string{
			fmt.Sprintf("(%s|%s)_[0-9]{1,2}", tok, t2),
			fmt.Sprintf("^line_[0-9]{1,2}:.*(%s)", tok),
			fmt.Sprintf("(%s|%s)", tok, t2),
		}
		return cf06QueryForm{Form: form, Pattern: templates[rng.Intn(len(templates))], Regex: true}
	case "glob_filtered":
		inc := []string{"*.txt", "*.go", "*.py", "*.ts", "*.md"}[rng.Intn(5)]
		return cf06QueryForm{Form: form, Pattern: tok, IncludeGlobs: []string{inc}, ExcludeGlobs: []string{"*.bin"}}
	default: // path_filtered
		prefix := "src/"
		if len(paths) > 0 {
			p := paths[rng.Intn(len(paths))]
			if seg, _, ok := strings.Cut(p, "/"); ok {
				prefix = seg + "/"
			}
		}
		return cf06QueryForm{Form: form, Pattern: tok, PathFilters: []string{prefix}}
	}
}

func cf06QueryToDSL(q cf06QueryForm) string {
	var parts []string
	if q.Regex {
		parts = append(parts, `"`+q.Pattern+`"`)
	} else {
		parts = append(parts, q.Pattern)
	}
	for _, pf := range q.PathFilters {
		parts = append(parts, "path:"+pf)
	}
	return strings.Join(parts, " ")
}

func TestCF06_S01_CodeLegDifferential(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Fatalf("ripgrep (rg) required for D3 differential: %v", err)
	}
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { _ = catalog.Drain(context.Background()) })

	const totalInstances = 3000
	const corpusCount = 60
	const queriesPerCorpus = totalInstances / corpusCount

	type corpusInfo struct {
		rootID string
		dir    string
		paths  []string
	}
	corpora := make([]corpusInfo, corpusCount)
	for c := 0; c < corpusCount; c++ {
		// Cover ledger range 1..500 files, with corpus 0=1 file and corpus 59=500 files.
		fileCount := 1 + (c*7)%35
		if c == 0 {
			fileCount = 1
		} else if c == corpusCount-1 {
			fileCount = 500
		} else if c%15 == 0 {
			fileCount = 120
		}
		dir, paths := cf06MakeCorpus(t, cf06BaseSeed, "S01", c, fileCount)
		rootID := fmt.Sprintf("s01-root-%02d", c)
		testutil.FailErr(t, "await s01 catalog", catalogtest.AwaitIndex(t.Context(), catalog, "p-s01", sourcecatalog.Root{ID: rootID, Path: dir}))
		corpora[c] = corpusInfo{rootID: rootID, dir: dir, paths: paths}
	}

	execCode := &CodeExecutor{catalog: catalog}
	var passed, failed, gapCount atomic.Int64
	var firstFailure string
	var failMu sync.Mutex

	workers := 6
	jobs := make(chan int, totalInstances)
	for i := 0; i < totalInstances; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				c := idx / queriesPerCorpus
				corp := corpora[c]
				rng := cf06RNG(cf06BaseSeed, "S01", idx)
				q := cf06BuildS01Query(rng, idx, corp.paths)

				refHits, err := cf06RunRipgrep(corp.dir, q, false)
				if err != nil {
					failed.Add(1)
					failMu.Lock()
					if firstFailure == "" {
						firstFailure = fmt.Sprintf("instance %d rg error: %v", idx, err)
					}
					failMu.Unlock()
					continue
				}

				dsl := cf06QueryToDSL(q)
				flags := MatchFlags{
					CaseSensitive: q.CaseSensitive,
					WholeWord:     q.WholeWord,
					Regex:         q.Regex,
					Include:       q.IncludeGlobs,
					Exclude:       q.ExcludeGlobs,
				}
				roots := []CodeRoot{{ProjectID: "p-s01", RootID: corp.rootID, Path: corp.dir}}
				plan, err := CompileQuery("project:current "+dsl, CompileContext{
					OriginProjectID: "p-s01",
					Flags:           flags,
					RootsForProject: func(string) ([]CodeRoot, error) { return roots, nil },
					Budget:          BudgetComplete,
				})
				if err != nil || plan.Code == nil {
					failed.Add(1)
					failMu.Lock()
					if firstFailure == "" {
						firstFailure = fmt.Sprintf("instance %d compile(%q) err=%v", idx, dsl, err)
					}
					failMu.Unlock()
					continue
				}
				plan.Code.Files = false
				plan.Code.Lines = true
				plan.Code.Cap = 50000

				report, err := execCode.Run(context.Background(), PlanLeg{Executor: ExecutorCode, Cap: 50000, Code: plan.Code})
				if err != nil {
					failed.Add(1)
					failMu.Lock()
					if firstFailure == "" {
						firstFailure = fmt.Sprintf("instance %d exec err=%v", idx, err)
					}
					failMu.Unlock()
					continue
				}
				prodHits := map[cf06RGLineHit]int{}
				for _, h := range report.Hits {
					if h.HitKind == HitKindCode {
						prodHits[cf06RGLineHit{Path: h.Path, Line: h.Line}]++
					}
				}
				// D4 check: extra hits are always a defect; missing hits only ok if declared gap.
				var extra []cf06RGLineHit
				for k := range prodHits {
					if refHits[k] == 0 {
						extra = append(extra, k)
					}
				}
				var missing []cf06RGLineHit
				for k := range refHits {
					if prodHits[k] == 0 {
						missing = append(missing, k)
					}
				}
				if len(extra) > 0 {
					failed.Add(1)
					failMu.Lock()
					if firstFailure == "" {
						firstFailure = fmt.Sprintf("instance %d (%s q=%q) extra hits=%v", idx, q.Form, dsl, extra[:min(3, len(extra))])
					}
					failMu.Unlock()
					continue
				}
				if len(missing) > 0 {
					issues := report.CoverageIssues(ExecutorCode)
					if report.Limited || len(issues) > 0 {
						gapCount.Add(1)
						passed.Add(1)
					} else {
						failed.Add(1)
						failMu.Lock()
						if firstFailure == "" {
							firstFailure = fmt.Sprintf("instance %d (%s q=%q) missing hits=%v without declared gap", idx, q.Form, dsl, missing[:min(3, len(missing))])
						}
						failMu.Unlock()
					}
					continue
				}
				passed.Add(1)
			}
		}()
	}
	wg.Wait()

	// 8 hand-written S01 edge cases.
	edgePassed := 0
	t.Run("edges", func(t *testing.T) {
		edgeRoot := t.TempDir()
		edgeFiles := map[string][]byte{
			"visible.txt":           []byte("alpha_edge\n"),
			".hidden/secret.txt":    []byte("alpha_edge\n"),
			"crlf.txt":              []byte("first\r\nalpha_edge\r\n"),
			"utf8bom.txt":           append([]byte{0xEF, 0xBB, 0xBF}, []byte("alpha_edge\n")...),
			"binary.bin":            {0x00, 0x01, 0x02, 'a', 'l', 'p', 'h', 'a', '_', 'e', 'd', 'g', 'e'},
			"src/case_mix.txt":      []byte("Alpha_Edge\nalpha_edge\n"),
			"src/whole_word.txt":    []byte("alpha_edge_suffix\nalpha_edge\n"),
		}
		for rel, content := range edgeFiles {
			abs := filepath.Join(edgeRoot, filepath.FromSlash(rel))
			testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
			testutil.FailErr(t, "write", os.WriteFile(abs, content, 0o644))
		}
		edgeRoot2 := t.TempDir()
		testutil.FailErr(t, "write root2", os.WriteFile(filepath.Join(edgeRoot2, "visible.txt"), []byte("alpha_edge\n"), 0o644))
		testutil.FailErr(t, "await edge1", catalogtest.AwaitIndex(t.Context(), catalog, "p-edge", sourcecatalog.Root{ID: "e1", Path: edgeRoot}))
		testutil.FailErr(t, "await edge2", catalogtest.AwaitIndex(t.Context(), catalog, "p-edge", sourcecatalog.Root{ID: "e2", Path: edgeRoot2}))

		runEdge := func(dsl string, flags MatchFlags, roots []CodeRoot) []Hit {
			plan, err := CompileQuery("project:current "+dsl, CompileContext{
				OriginProjectID: "p-edge",
				Flags:           flags,
				RootsForProject: func(string) ([]CodeRoot, error) { return roots, nil },
				Budget:          BudgetComplete,
			})
			testutil.FailErr(t, "compile "+dsl, err)
			plan.Code.Files = false
			plan.Code.Lines = true
			rep, err := execCode.Run(context.Background(), PlanLeg{Executor: ExecutorCode, Cap: 1000, Code: plan.Code})
			testutil.FailErr(t, "run "+dsl, err)
			return rep.Hits
		}
		r1 := []CodeRoot{{ProjectID: "p-edge", RootID: "e1", Path: edgeRoot}}

		// Edge 1: hidden excluded by default
		h1 := hitPathsOfKind(runEdge("alpha_edge", MatchFlags{}, r1), HitKindCode)
		if slices.Contains(h1, ".hidden/secret.txt") || !slices.Contains(h1, "visible.txt") {
			t.Fatalf("edge1 hidden default exclusion failed: %v", h1)
		}
		edgePassed++

		// Edge 2: hidden admitted by positive path: filter
		h2 := hitPathsOfKind(runEdge("alpha_edge path:.hidden/", MatchFlags{}, r1), HitKindCode)
		if !slices.Contains(h2, ".hidden/secret.txt") {
			t.Fatalf("edge2 positive path filter failed: %v", h2)
		}
		edgePassed++

		// Edge 3: hidden admitted by positive include glob
		h3 := hitPathsOfKind(runEdge("alpha_edge", MatchFlags{Include: []string{".hidden/*.txt"}}, r1), HitKindCode)
		if !slices.Contains(h3, ".hidden/secret.txt") {
			t.Fatalf("edge3 positive include glob failed: %v", h3)
		}
		edgePassed++

		// Edge 4: negative filter does not widen hidden discovery
		h4 := hitPathsOfKind(runEdge("alpha_edge NOT path:src/", MatchFlags{}, r1), HitKindCode)
		if slices.Contains(h4, ".hidden/secret.txt") {
			t.Fatalf("edge4 negative filter widened hidden discovery: %v", h4)
		}
		edgePassed++

		// Edge 5: CRLF line numbering
		hits5 := runEdge("alpha_edge path:crlf.txt", MatchFlags{}, r1)
		if len(hits5) != 1 || hits5[0].Line != 2 {
			t.Fatalf("edge5 CRLF line number failed: %+v", hits5)
		}
		edgePassed++

		// Edge 6: UTF-8 BOM decoded cleanly
		hits6 := runEdge("alpha_edge path:utf8bom.txt", MatchFlags{}, r1)
		if len(hits6) != 1 || hits6[0].Line != 1 {
			t.Fatalf("edge6 UTF-8 BOM failed: %+v", hits6)
		}
		edgePassed++

		// Edge 7: Binary file skipped (not matched as text)
		if slices.Contains(h1, "binary.bin") {
			t.Fatalf("edge7 binary file matched: %v", h1)
		}
		edgePassed++

		// Edge 8: Multi-root distinct root IDs
		rBoth := []CodeRoot{
			{ProjectID: "p-edge", RootID: "e1", Path: edgeRoot},
			{ProjectID: "p-edge", RootID: "e2", Path: edgeRoot2},
		}
		hits8 := runEdge("alpha_edge path:visible.txt", MatchFlags{}, rBoth)
		if len(hits8) != 2 || hits8[0].RootID == hits8[1].RootID {
			t.Fatalf("edge8 multi-root distinct IDs failed: %+v", hits8)
		}
		edgePassed++
	})

	t.Logf("CF06_SUMMARY {\"family\":\"S01\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":%d,\"edges_planned\":8,\"edges_passed\":%d}",
		totalInstances, passed.Load()+failed.Load(), passed.Load(), failed.Load(), gapCount.Load(), edgePassed)
	if failed.Load() > 0 {
		t.Fatalf("S01 differential failed %d/%d instances; first: %s", failed.Load(), totalInstances, firstFailure)
	}
}

func TestCF06_S02_LiteralAcceleration(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Fatalf("ripgrep (rg) required for S02 D3 reference: %v", err)
	}
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { _ = catalog.Drain(context.Background()) })

	const totalInstances = 3000
	const corpusCount = 30
	const queriesPerCorpus = totalInstances / corpusCount

	type s02Corpus struct {
		rootID string
		dir    string
		root   sourcecatalog.Root
	}
	corpora := make([]s02Corpus, corpusCount)
	for c := 0; c < corpusCount; c++ {
		fileCount := 1 + (c*11)%40
		if c == 0 {
			fileCount = 1
		} else if c == corpusCount-1 {
			fileCount = 500
		}
		dir, _ := cf06MakeCorpus(t, cf06BaseSeed, "S02", c, fileCount)
		root := sourcecatalog.Root{ID: fmt.Sprintf("s02-root-%02d", c), Path: dir}
		testutil.FailErr(t, "await s02 catalog", catalogtest.AwaitIndex(t.Context(), catalog, "p-s02", root))
		corpora[c] = s02Corpus{rootID: root.ID, dir: dir, root: root}
	}

	var passed, failed atomic.Int64
	var firstFailure string

	allScope := sourcecatalog.FileScope{Audience: sourcecatalog.HumanAudience, IncludeHidden: true}
	for c := 0; c < corpusCount; c++ {
		corp := corpora[c]
		reader, _, err := catalog.Trees.OpenIndex(t.Context(), "p-s02", corp.root, 0)
		testutil.FailErr(t, "open index", err)
		var entries []sourcecatalog.Entry
		afterPath := ""
		for {
			pageEntries, err := reader.FilePage(t.Context(), allScope, afterPath, sourcecatalog.TreeFilePageLimit)
			testutil.FailErr(t, "file page", err)
			if len(pageEntries) == 0 {
				break
			}
			entries = append(entries, pageEntries...)
			afterPath = pageEntries[len(pageEntries)-1].Path
		}
		openFn := func(e sourcecatalog.Entry) (io.ReadCloser, error) {
			return os.Open(filepath.Join(corp.dir, filepath.FromSlash(e.Path)))
		}
		// Prime blooms via one pass of IndexLiteralCandidates.
		_ = catalog.Literals.IndexLiteralCandidates(t.Context(), reader, sourcecatalog.LiteralQuery{
			IncludeKey: "s02-all",
			FileScope:  allScope,
			Require:    litprefilter.AllOf("alpha"),
			Open:       openFn,
		}, entries)

		for qIdx := 0; qIdx < queriesPerCorpus; qIdx++ {
			idx := c*queriesPerCorpus + qIdx
			rng := cf06RNG(cf06BaseSeed, "S02", idx)
			tok := cf06Tokens[rng.Intn(len(cf06Tokens))]
			if rng.Intn(4) == 0 {
				tok = strings.ToUpper(tok)
			}
			refHits, err := cf06RunRipgrep(corp.dir, cf06QueryForm{Form: "literal", Pattern: tok}, true)
			if err != nil {
				failed.Add(1)
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d rg err: %v", idx, err)
				}
				continue
			}
			refFiles := map[string]bool{}
			for h := range refHits {
				refFiles[h.Path] = true
			}
			page := catalog.Literals.IndexLiteralCandidates(t.Context(), reader, sourcecatalog.LiteralQuery{
				IncludeKey: "s02-all",
				FileScope:  allScope,
				Require:    litprefilter.AllOf(tok),
				Open:       openFn,
			}, entries)
			candSet := map[string]bool{}
			for _, e := range page.Candidates {
				candSet[e.Path] = true
			}
			skipped := ""
			for f := range refFiles {
				if !candSet[f] {
					skipped = f
					break
				}
			}
			if skipped != "" {
				failed.Add(1)
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d literal %q skipped matching file %q", idx, tok, skipped)
				}
			} else {
				passed.Add(1)
			}
		}
		_ = reader.Close()
	}

	// 3 hand-written S02 edges.
	edgePassed := 0
	t.Run("edges", func(t *testing.T) {
		dir := t.TempDir()
		fPath := filepath.Join(dir, "target.txt")
		testutil.FailErr(t, "write initial", os.WriteFile(fPath, []byte("initial_content_only\n"), 0o644))
		root := sourcecatalog.Root{ID: "s02-edge", Path: dir}
		testutil.FailErr(t, "await edge", catalogtest.AwaitIndex(t.Context(), catalog, "p-s02-edge", root))

		// Edge 1: Edit file after indexing and search before catalog re-index -> must still match via live read.
		testutil.FailErr(t, "edit file", os.WriteFile(fPath, []byte("freshly_edited_token\n"), 0o644))
		hits := runCodeLegWithCatalog(t, catalog, &CodePlanLeg{
			Query:     TextExpr{Text: "freshly_edited_token"},
			PathRoots: []CodeRoot{{ProjectID: "p-s02-edge", RootID: "s02-edge", Path: dir}},
			Lines:     true,
			Cap:       100,
		})
		if len(hits) != 1 || hits[0].Path != "target.txt" {
			t.Fatalf("edge1 edited-before-reindex missed: %+v", hits)
		}
		edgePassed++

		// Edge 2: Unindexed file (fresh catalog with cold bloom cache) remains candidate and scans directly.
		freshCat := sourcecatalog.New()
		t.Cleanup(func() { _ = freshCat.Drain(context.Background()) })
		testutil.FailErr(t, "await fresh", catalogtest.AwaitIndex(t.Context(), freshCat, "p-s02-edge2", root))
		r2, _, err := freshCat.Trees.OpenIndex(t.Context(), "p-s02-edge2", root, 0)
		testutil.FailErr(t, "open fresh index", err)
		ents, err := r2.FilePage(t.Context(), allScope, "", 100)
		testutil.FailErr(t, "page fresh", err)
		page := freshCat.Literals.IndexLiteralCandidates(t.Context(), r2, sourcecatalog.LiteralQuery{
			IncludeKey: "cold",
			FileScope:  allScope,
			Require:    litprefilter.AllOf("freshly_edited_token"),
			Open:       func(e sourcecatalog.Entry) (io.ReadCloser, error) { return os.Open(filepath.Join(dir, e.Path)) },
		}, ents)
		_ = r2.Close()
		if len(page.Candidates) != len(ents) {
			t.Fatalf("edge2 unindexed file skipped: candidates=%d want=%d", len(page.Candidates), len(ents))
		}
		edgePassed++

		// Edge 3: Short (<3 byte) literal & mixed case never skipped by 3-gram bloom.
		testutil.FailErr(t, "write short", os.WriteFile(fPath, []byte("XyZ_short_ab\n"), 0o644))
		hits3 := runCodeLegWithCatalog(t, catalog, &CodePlanLeg{
			Query:     TextExpr{Text: "ab"},
			PathRoots: []CodeRoot{{ProjectID: "p-s02-edge", RootID: "s02-edge", Path: dir}},
			Lines:     true,
			Cap:       100,
		})
		if len(hits3) != 1 || hits3[0].Path != "target.txt" {
			t.Fatalf("edge3 short literal missed: %+v", hits3)
		}
		edgePassed++
	})

	t.Logf("CF06_SUMMARY {\"family\":\"S02\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":0,\"edges_planned\":3,\"edges_passed\":%d}",
		totalInstances, passed.Load()+failed.Load(), passed.Load(), failed.Load(), edgePassed)
	if failed.Load() > 0 {
		t.Fatalf("S02 literal acceleration failed %d/%d instances; first: %s", failed.Load(), totalInstances, firstFailure)
	}
}

func runCodeLegWithCatalog(t *testing.T, catalog *sourcecatalog.Catalog, leg *CodePlanLeg) []Hit {
	t.Helper()
	report, err := (&CodeExecutor{catalog: catalog, rerank: decide.Reranker{}}).Run(context.Background(), PlanLeg{
		Executor: ExecutorCode,
		Cap:      leg.Cap,
		Code:     leg,
	})
	testutil.FailErr(t, "run code leg", err)
	return report.Hits
}
