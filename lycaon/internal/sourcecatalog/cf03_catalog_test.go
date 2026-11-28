package sourcecatalog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

const cf03BaseSeed = "20261127"

type cf03Summary struct {
	Family       string         `json:"family"`
	Lane         string         `json:"lane"`
	BaseSeed     string         `json:"base_seed"`
	Platform     string         `json:"platform"`
	CaseSensitive bool          `json:"case_sensitive"`
	Planned      int            `json:"planned"`
	Executed     int            `json:"executed"`
	Passed       int            `json:"passed"`
	Failed       int            `json:"failed"`
	DeclaredGaps map[string]int `json:"declared_gaps"`
}

func cf03SeedRNG(baseSeed, family string, instance int) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", baseSeed, family, instance)))
	s1 := binary.BigEndian.Uint64(sum[:8])
	s2 := binary.BigEndian.Uint64(sum[8:16])
	return rand.New(rand.NewPCG(s1, s2))
}

func cf03LogSummary(t *testing.T, s cf03Summary) {
	t.Helper()
	raw, err := json.Marshal(s)
	testutil.FailErr(t, "marshal cf03 summary", err)
	t.Logf("CF03_SUMMARY:%s", string(raw))
}

func cf03ProbeCaseSensitive(t *testing.T, dir string) bool {
	t.Helper()
	lower := filepath.Join(dir, "cf03_case_probe_a.tmp")
	upper := filepath.Join(dir, "CF03_CASE_PROBE_A.tmp")
	testutil.FailErr(t, "write case probe", os.WriteFile(lower, []byte("1"), 0o600))
	defer func() { _ = os.Remove(lower) }()
	_, err := os.Stat(upper)
	return errors.Is(err, os.ErrNotExist)
}

func cf03AllIndexedPaths(t *testing.T, r *IndexReader, scope FileScope) []string {
	t.Helper()
	var out []string
	cursor := ""
	for {
		page, err := r.FilePathsPage(t.Context(), scope, cursor, TreeFilePageLimit)
		testutil.FailErr(t, "page indexed files", err)
		out = append(out, page...)
		if len(page) < TreeFilePageLimit {
			break
		}
		cursor = page[len(page)-1]
	}
	return out
}

// cf03RefWalk is an independent D3 reference walk using only stdlib os.ReadDir/os.Lstat.
func cf03RefWalkFiles(t *testing.T, root string, boundaryNames map[string]bool, agentAudience bool) []string {
	t.Helper()
	var files []string
	var walk func(absDir, relPrefix string)
	walk = func(absDir, relPrefix string) {
		entries, err := os.ReadDir(absDir)
		testutil.FailErr(t, "ref readdir "+absDir, err)
		for _, entry := range entries {
			name := entry.Name()
			rel := name
			if relPrefix != "" {
				rel = relPrefix + "/" + name
			}
			full := filepath.Join(absDir, name)
			info, err := os.Lstat(full)
			testutil.FailErr(t, "ref lstat "+full, err)
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if info.IsDir() {
				if boundaryNames[name] {
					continue
				}
				// Nested checkout boundary: directory containing .git (other than root .git).
				if name != ".git" {
					if _, gitErr := os.Lstat(filepath.Join(full, ".git")); gitErr == nil {
						continue
					}
				}
				walk(full, rel)
				continue
			}
			if info.Mode().IsRegular() {
				if agentAudience && (rel == ".paintedwolf" || strings.HasPrefix(rel, ".paintedwolf/")) {
					continue
				}
				files = append(files, rel)
			}
		}
	}
	walk(root, "")
	sort.Strings(files)
	return files
}

func cf03EntryCountForInstance(rng *rand.Rand, idx int) int {
	switch {
	case idx == 0:
		return 1
	case idx == 1:
		return 2000
	case idx%250 == 0:
		return 200 + rng.IntN(400)
	case idx%50 == 0:
		return 40 + rng.IntN(80)
	default:
		return 2 + rng.IntN(14)
	}
}

func cf03PickDir(rng *rand.Rand, maxDepth int) string {
	depth := rng.IntN(maxDepth)
	if depth == 0 {
		return ""
	}
	roots := []string{"src", "pkg", "lib", "docs", "cmd", "internal"}
	parts := make([]string, 0, depth)
	parts = append(parts, roots[rng.IntN(len(roots))])
	for level := 1; level < depth; level++ {
		parts = append(parts, fmt.Sprintf("d%d_%d", level, rng.IntN(3)))
	}
	return strings.Join(parts, "/")
}

// TestCF03_K01_Membership runs 2,000 seeded in-process K01 instances (1-2,000 entries, depth 1-8)
// comparing settled sourcecatalog membership against an independent reference walk, including
// hidden/ignored, empty dirs, VCS metadata, boundary dirs, nested checkouts, directory symlinks,
// .paintedwolf human vs agent projections, and an editor-only root edge.
func TestCF03_K01_Membership(t *testing.T) {
	const planned = 2000
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)
	caseSens := cf03ProbeCaseSensitive(t, cfgDir)

	boundaryNames := map[string]bool{
		".git": true, "node_modules": true, "build": true, "dist": true,
		"target": true, "__pycache__": true, ".venv": true, ".cache": true,
	}
	boundaryChoices := []string{"node_modules", "build", "dist", "target", "__pycache__", ".venv", ".cache"}
	exts := []string{".go", ".ts", ".py", ".md", ".txt"}

	passed := 0
	for idx := range planned {
		rng := cf03SeedRNG(cf03BaseSeed, "K01", idx)
		maxDepth := 1 + rng.IntN(8)
		targetCount := cf03EntryCountForInstance(rng, idx)

		c := New()
		rootDir := t.TempDir()
		root := Root{ID: fmt.Sprintf("k01-%d", idx), Path: rootDir}

		if idx == 0 {
			// Minimal 1-entry tree.
			writeIndexFile(t, rootDir, "only.txt", "one")
		} else {
			if idx%3 == 0 {
				writeIndexFile(t, rootDir, ".git/HEAD", "ref: refs/heads/main\n")
			}
			if idx%4 == 0 {
				writeIndexFile(t, rootDir, ".gitignore", "*.ignored\nignored_dir/\n")
				writeIndexFile(t, rootDir, "scratch.ignored", "ignored")
				writeIndexFile(t, rootDir, "ignored_dir/cache.txt", "ignored-dir")
			}
			if idx%5 == 0 {
				writeIndexFile(t, rootDir, ".hidden_file.txt", "hidden")
				testutil.FailErr(t, "empty dir", os.MkdirAll(filepath.Join(rootDir, "empty_dir"), 0o755))
			}
			if idx%6 == 0 {
				bname := boundaryChoices[rng.IntN(len(boundaryChoices))]
				writeIndexFile(t, rootDir, bname+"/artifact.txt", "boundary")
			}
			if idx%7 == 0 {
				writeIndexFile(t, rootDir, "nested_repo/.git/HEAD", "ref: refs/heads/dev\n")
				writeIndexFile(t, rootDir, "nested_repo/inside.go", "package nested")
			}
			if idx%8 == 0 {
				writeIndexFile(t, rootDir, ".paintedwolf/notes.md", "human visible")
			}
			if idx%11 == 0 {
				writeIndexFile(t, rootDir, "symtarget/real.txt", "real")
				testutil.FailErr(t, "dir symlink", os.Symlink("symtarget", filepath.Join(rootDir, "link_dir")))
			}
			for f := range targetCount {
				dir := cf03PickDir(rng, maxDepth)
				name := fmt.Sprintf("f_%04d%s", f, exts[rng.IntN(len(exts))])
				rel := name
				if dir != "" {
					rel = dir + "/" + name
				}
				writeIndexFile(t, rootDir, rel, "body")
			}
		}

		reader := waitIndex(t, c, root)
		cov, err := reader.Coverage(t.Context())
		testutil.FailErr(t, "k01 coverage", err)
		if !cov.Exhaustive() {
			t.Fatalf("K01[%d]: expected exhaustive coverage, got %+v", idx, cov)
		}

		gotHuman := cf03AllIndexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true})
		wantHuman := cf03RefWalkFiles(t, rootDir, boundaryNames, false)
		if !slices.Equal(gotHuman, wantHuman) {
			t.Fatalf("K01[%d] human mismatch: got=%d want=%d (first diff got=%v want=%v)", idx, len(gotHuman), len(wantHuman), gotHuman[:min(5, len(gotHuman))], wantHuman[:min(5, len(wantHuman))])
		}

		gotAgent := cf03AllIndexedPaths(t, reader, FileScope{Audience: AgentAudience, IncludeHidden: true})
		wantAgent := cf03RefWalkFiles(t, rootDir, boundaryNames, true)
		if !slices.Equal(gotAgent, wantAgent) {
			t.Fatalf("K01[%d] agent mismatch: got=%d want=%d", idx, len(gotAgent), len(wantAgent))
		}

		testutil.FailErr(t, "close reader", reader.Close())
		testutil.FailErr(t, "drain catalog", c.Drain(context.Background()))
		_ = os.RemoveAll(rootDir)
		passed++
	}

	// Editor-only root edge: boundary at the root, zero eager files listed.
	facts := rootfacts.Install(t)
	cEd := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain editor-only", cEd.Drain(context.Background())) })
	for _, er := range facts.EditorOnly() {
		writeIndexFile(t, er.Path, "notes/item.txt", "ed")
		cr := Root{ID: er.Name, Path: er.Path}
		r := waitIndex(t, cEd, cr)
		if got := cf03AllIndexedPaths(t, r, FileScope{Audience: HumanAudience, IncludeHidden: true}); len(got) != 0 {
			t.Fatalf("editor-only root %s eagerly listed %v", er.Name, got)
		}
	}

	cf03LogSummary(t, cf03Summary{
		Family:        "K01",
		Lane:          "in-process",
		BaseSeed:      cf03BaseSeed,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		CaseSensitive: caseSens,
		Planned:       planned,
		Executed:      passed,
		Passed:        passed,
		Failed:        0,
		DeclaredGaps:  map[string]int{},
	})
}

// TestCF03_K02_CoverageTruth runs 600 seeded in-process K02 instances across all 6 coverage
// states (cold, partly_discovered, over_budget, unreadable_dir, failed_refresh, unavailable)
// and 1-3 roots, asserting declared facts, lower-bound counts, healthy-beside-failing isolation,
// and that no state produces a complete-looking empty answer.
func TestCF03_K02_CoverageTruth(t *testing.T) {
	const planned = 600
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)
	caseSens := cf03ProbeCaseSensitive(t, cfgDir)

	states := []string{"cold", "partly_discovered", "over_budget", "unreadable_dir", "failed_refresh", "unavailable"}
	gapCounts := map[string]int{}
	passed := 0

	for idx := range planned {
		rng := cf03SeedRNG(cf03BaseSeed, "K02", idx)
		state := states[idx%len(states)]
		extraHealthyRoots := rng.IntN(3) // 0..2 healthy sibling roots (total 1..3 roots)

		c := New()
		rootDir := t.TempDir()
		root := Root{ID: fmt.Sprintf("k02-target-%d", idx), Path: rootDir}

		// Optionally create healthy sibling roots alongside the target root.
		var healthyRoots []Root
		for h := range extraHealthyRoots {
			hDir := t.TempDir()
			writeIndexFile(t, hDir, fmt.Sprintf("healthy_%d.txt", h), "ok")
			hr := Root{ID: fmt.Sprintf("k02-healthy-%d-%d", idx, h), Path: hDir}
			hrReader := waitIndex(t, c, hr)
			hCov, err := hrReader.Coverage(t.Context())
			testutil.FailErr(t, "healthy coverage", err)
			if !hCov.Exhaustive() {
				t.Fatalf("K02[%d] healthy root not exhaustive: %+v", idx, hCov)
			}
			if got := cf03AllIndexedPaths(t, hrReader, FileScope{Audience: HumanAudience, IncludeHidden: true}); len(got) != 1 {
				t.Fatalf("K02[%d] healthy root files=%v", idx, got)
			}
			testutil.FailErr(t, "close healthy reader", hrReader.Close())
			healthyRoots = append(healthyRoots, hr)
		}

		switch state {
		case "cold":
			writeIndexFile(t, rootDir, "a/one.txt", "1")
			s, w := indexWalkFixture(t, c, root)
			if _, err := openIndexReader(t, s); err == nil {
				t.Fatalf("K02[%d] cold: seeding published a complete-looking empty generation", idx)
			}
			w.discard()
			gapCounts["catalog_warming"]++

		case "partly_discovered":
			writeIndexFile(t, rootDir, "dir_a/one.txt", "1")
			writeIndexFile(t, rootDir, "dir_b/two.txt", "2")
			s, w := indexWalkFixture(t, c, root)
			_ = stepIndexDir(t, w) // "."
			_ = stepIndexDir(t, w) // "dir_a" only
			r, err := openIndexReader(t, s)
			testutil.FailErr(t, "open partial reader", err)
			cov, err := r.Coverage(t.Context())
			testutil.FailErr(t, "partial coverage", err)
			if cov.DiscoveryComplete || cov.Exhaustive() || !cov.Pending() {
				t.Fatalf("K02[%d] partly_discovered: bad coverage %+v", idx, cov)
			}
			cnt, err := r.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
			testutil.FailErr(t, "partial count", err)
			if cnt > 2 {
				t.Fatalf("K02[%d] partly_discovered: count %d exceeds truth 2", idx, cnt)
			}
			testutil.FailErr(t, "close partial", r.Close())
			w.discard()
			gapCounts["catalog_incomplete"]++

		case "over_budget":
			c.SetScopes(testScopes{plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{DirectoryEntries: 2}}})
			writeIndexFile(t, rootDir, "visible.txt", "v")
			for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
				writeIndexFile(t, rootDir, "wide/"+name, "w")
			}
			r := waitIndex(t, c, root)
			cov, err := r.Coverage(t.Context())
			testutil.FailErr(t, "over_budget coverage", err)
			if cov.BoundedDirectories < 1 || cov.Exhaustive() {
				t.Fatalf("K02[%d] over_budget: expected bounded dir, got %+v", idx, cov)
			}
			rf, err := c.Trees.RootFileCount(t.Context(), "p", root, FileScope{Audience: HumanAudience, IncludeHidden: true}, 0)
			testutil.FailErr(t, "over_budget count", err)
			if rf.Measured || rf.Count > 4 {
				t.Fatalf("K02[%d] over_budget: count should be unmeasured lower bound, got %+v", idx, rf)
			}
			testutil.FailErr(t, "close over_budget", r.Close())
			gapCounts["catalog_bounded"]++

		case "unreadable_dir":
			writeIndexFile(t, rootDir, "good.txt", "ok")
			s, w := indexWalkFixture(t, c, root)
			testutil.FailErr(t, "fault dir", w.faultDir(t.Context(), "private", os.ErrPermission))
			w.covering = true
			testutil.FailErr(t, "publish fault", w.publish(t.Context()))
			r, err := openIndexReader(t, s)
			testutil.FailErr(t, "open fault reader", err)
			cov, err := r.Coverage(t.Context())
			testutil.FailErr(t, "fault coverage", err)
			if cov.FailedDirectories < 1 || cov.Exhaustive() {
				t.Fatalf("K02[%d] unreadable_dir: expected FailedDirectories>=1, got %+v", idx, cov)
			}
			testutil.FailErr(t, "close fault", r.Close())
			w.discard()
			gapCounts["catalog_failed"]++

		case "failed_refresh":
			writeIndexFile(t, rootDir, "good.txt", "ok")
			first := waitIndex(t, c, root)
			release, err := c.Trees.broker.Acquire(t.Context(), backgroundwork.Request{
				Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
			})
			testutil.FailErr(t, "hold broker", err)
			first.store.mu.Lock()
			first.store.status.Error = "failed refresh"
			first.store.full = true
			first.store.mu.Unlock()
			r, _, err := c.Trees.OpenIndex(t.Context(), "p", root, 0)
			testutil.FailErr(t, "open failed refresh", err)
			if r == nil {
				release()
				t.Fatalf("K02[%d] failed_refresh: lost readable generation", idx)
			}
			cov, err := r.Coverage(t.Context())
			testutil.FailErr(t, "failed_refresh coverage", err)
			if cov.Error == "" || cov.Exhaustive() {
				release()
				t.Fatalf("K02[%d] failed_refresh: expected error & !Exhaustive, got %+v", idx, cov)
			}
			testutil.FailErr(t, "close failed_refresh", r.Close())
			testutil.FailErr(t, "close first", first.Close())
			release()
			gapCounts["catalog_refresh_failed"]++

		case "unavailable":
			missingRoot := Root{ID: fmt.Sprintf("k02-missing-%d", idx), Path: filepath.Join(rootDir, "does-not-exist")}
			s, err := c.Trees.indexStore(t.Context(), "p", missingRoot)
			testutil.FailErr(t, "unavailable store", err)
			recErr := s.reconcile(t.Context(), repochange.CurrentEpoch(missingRoot.Path))
			if recErr == nil {
				t.Fatalf("K02[%d] unavailable: expected reconcile error on missing root", idx)
			}
			if _, openErr := openIndexReader(t, s); openErr == nil {
				t.Fatalf("K02[%d] unavailable: missing root yielded complete-looking empty generation", idx)
			}
			gapCounts["catalog_failed"]++
		}

		// Verify healthy siblings still answer cleanly after target root's degraded state.
		for _, hr := range healthyRoots {
			hrReader, status, err := c.Trees.OpenIndex(t.Context(), "p", hr, 0)
			testutil.FailErr(t, "reopen healthy sibling", err)
			if hrReader == nil || !status.Complete {
				t.Fatalf("K02[%d] healthy sibling degraded beside %s: status=%+v", idx, state, status)
			}
			testutil.FailErr(t, "close healthy sibling", hrReader.Close())
			_ = os.RemoveAll(hr.Path)
		}

		testutil.FailErr(t, "drain k02", c.Drain(context.Background()))
		_ = os.RemoveAll(rootDir)
		passed++
	}

	cf03LogSummary(t, cf03Summary{
		Family:        "K02",
		Lane:          "in-process",
		BaseSeed:      cf03BaseSeed,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		CaseSensitive: caseSens,
		Planned:       planned,
		Executed:      passed,
		Passed:        passed,
		Failed:        0,
		DeclaredGaps:  gapCounts,
	})
}

// TestCF03_K02_INC03_ColdWalkCoverage replays incident slot K02-INC-03:
// 2,000+ entry synthetic tree queried while initial walk has no completed generation.
func TestCF03_K02_INC03_ColdWalkCoverage(t *testing.T) {
	c, root := indexFixture(t)
	for i := range 2050 {
		writeIndexFile(t, root.Path, fmt.Sprintf("pkg_%02d/file_%04d.go", i%20, i), "package p\n")
	}
	s, w := indexWalkFixture(t, c, root)
	defer w.discard()

	// Before any directory is published (completed_generation=0), no complete-looking empty answer.
	if _, err := openIndexReader(t, s); err == nil {
		t.Fatal("K02-INC-03: cold walk with completed_generation=0 published an empty generation")
	}
	// Step only the root directory "." so walk is actively scanning (partial).
	if dir := stepIndexDir(t, w); dir != "." {
		t.Fatalf("K02-INC-03: expected first frontier dir '.', got %q", dir)
	}
	_ = stepIndexDir(t, w) // step one child dir
	r, err := openIndexReader(t, s)
	testutil.FailErr(t, "K02-INC-03 open active walk", err)
	cov, err := r.Coverage(t.Context())
	testutil.FailErr(t, "K02-INC-03 coverage", err)
	if cov.DiscoveryComplete || cov.Exhaustive() || !cov.Pending() {
		t.Fatalf("K02-INC-03: active first walk must report !DiscoveryComplete & Pending, got %+v", cov)
	}
	cnt, err := r.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "K02-INC-03 partial count", err)
	if cnt == 0 || cnt >= 2050 {
		t.Fatalf("K02-INC-03: expected partial count in (0,2050), got %d", cnt)
	}
}

// TestCF03_K02_INC04_UnreadableAndUnconfinedCoexistence replays incident slot K02-INC-04:
// chmod 000 unreadable subtree + symlink pointing outside root coexisting with healthy files.
func TestCF03_K02_INC04_UnreadableAndUnconfinedCoexistence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 is bypassed as root")
	}
	c, root := indexFixture(t)
	outside := t.TempDir()
	writeIndexFile(t, outside, "secret_outside.txt", "must-not-be-read")
	writeIndexFile(t, root.Path, "healthy/ok.txt", "healthy")
	unreadableDir := filepath.Join(root.Path, "locked")
	writeIndexFile(t, root.Path, "locked/hidden.txt", "locked")
	testutil.FailErr(t, "symlink outside", os.Symlink(outside, filepath.Join(root.Path, "escape_link")))
	testutil.FailErr(t, "chmod 000", os.Chmod(unreadableDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadableDir, 0o755) })

	r := waitIndex(t, c, root)
	cov, err := r.Coverage(t.Context())
	testutil.FailErr(t, "K02-INC-04 coverage", err)
	if cov.FailedDirectories < 1 || cov.Exhaustive() {
		t.Fatalf("K02-INC-04: expected FailedDirectories>=1 & !Exhaustive, got %+v", cov)
	}
	paths := cf03AllIndexedPaths(t, r, FileScope{Audience: HumanAudience, IncludeHidden: true})
	if !slices.Contains(paths, "healthy/ok.txt") {
		t.Fatalf("K02-INC-04: healthy file missing alongside unreadable dir: %v", paths)
	}
	for _, p := range paths {
		if strings.Contains(p, "secret_outside") || strings.Contains(p, "escape_link/") {
			t.Fatalf("K02-INC-04: unconfined outside path leaked into catalog: %s", p)
		}
	}
}

// TestCF03_K03_GenerationsAndRestart runs 600 seeded in-process K03 instances covering:
// 1) completed generation stays readable during refresh,
// 2) mid-walk restart resumes from persisted frontier without re-reading finished dirs by count,
// 3) paging/retaining across a generation change never crosses generations.
func TestCF03_K03_GenerationsAndRestart(t *testing.T) {
	const planned = 600
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)
	caseSens := cf03ProbeCaseSensitive(t, cfgDir)

	passed := 0
	for idx := range planned {
		rng := cf03SeedRNG(cf03BaseSeed, "K03", idx)
		mode := idx % 3
		c := New()
		rootDir := t.TempDir()
		root := Root{ID: fmt.Sprintf("k03-%d", idx), Path: rootDir}

		switch mode {
		case 0:
			// Completed generation stays readable while refresh is held at broker barrier.
			// Decision 107 (K03-555): verify pinned gen1 reader never drifts AND join root + generation identity
			// for any fresh OpenIndex read during refresh (must be exact gen1 or exact newly published gen2, never partial).
			nFiles := 2 + rng.IntN(20)
			for i := range nFiles {
				writeIndexFile(t, rootDir, fmt.Sprintf("src/f_%03d.go", i), "v1")
			}
			gen1 := waitIndex(t, c, root)
			gen1Rev := gen1.Status.Revision
			gen1Inst := gen1.Status.Instance
			gen1Files := cf03AllIndexedPaths(t, gen1, FileScope{Audience: HumanAudience, IncludeHidden: true})
			if len(gen1Files) != nFiles {
				t.Fatalf("K03[%d] root=%s gen1(%d:%d) count=%d want %d", idx, root.ID, gen1Inst, gen1Rev, len(gen1Files), nFiles)
			}
			// Hold metadata lane so background refresh is blocked.
			release, err := c.Trees.broker.Acquire(t.Context(), backgroundwork.Request{
				Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
			})
			testutil.FailErr(t, "hold refresh", err)
			writeIndexFile(t, rootDir, "src/added_during_refresh.go", "v2")
			c.InvalidateRoot(root.Path, "src/added_during_refresh.go")

			// 1) Already-opened gen1 reader MUST remain pinned to gen1Files (zero drift).
			pinnedFiles := cf03AllIndexedPaths(t, gen1, FileScope{Audience: HumanAudience, IncludeHidden: true})
			if !slices.Equal(pinnedFiles, gen1Files) {
				release()
				t.Fatalf("K03[%d] root=%s pinned gen1(%d:%d) drifted: %d vs %d", idx, root.ID, gen1Inst, gen1Rev, len(pinnedFiles), len(gen1Files))
			}

			// 2) Fresh OpenIndex read must return a complete generation (either gen1 or newly published gen2 with rev != gen1Rev).
			during, _, err := c.Trees.OpenIndex(t.Context(), "p", root, 0)
			testutil.FailErr(t, "open during refresh", err)
			if during == nil {
				release()
				t.Fatalf("K03[%d] root=%s completed generation became unreadable during refresh", idx, root.ID)
			}
			duringFiles := cf03AllIndexedPaths(t, during, FileScope{Audience: HumanAudience, IncludeHidden: true})
			wantGen2 := append(slices.Clone(gen1Files), "src/added_during_refresh.go")
			sort.Strings(wantGen2)
			if during.Status.Revision == gen1Rev && during.Status.Instance == gen1Inst {
				if !slices.Equal(duringFiles, gen1Files) {
					release()
					t.Fatalf("K03[%d] root=%s same gen(%d:%d) drifted mid-refresh: %d vs %d", idx, root.ID, gen1Inst, gen1Rev, len(duringFiles), len(gen1Files))
				}
			} else if !slices.Equal(duringFiles, wantGen2) {
				release()
				t.Fatalf("K03[%d] root=%s new gen(%d:%d) incomplete/corrupt: got=%d want=%d", idx, root.ID, during.Status.Instance, during.Status.Revision, len(duringFiles), len(wantGen2))
			}
			testutil.FailErr(t, "close during", during.Close())
			testutil.FailErr(t, "close gen1", gen1.Close())
			release()

		case 1:
			// Mid-walk restart resumes from persisted frontier without re-reading finished dirs by count.
			dirCount := 3 + rng.IntN(4)
			for d := range dirCount {
				writeIndexFile(t, rootDir, fmt.Sprintf("d%02d/item.txt", d), "body")
			}
			s1, w1 := indexWalkFixture(t, c, root)
			visitedBefore := map[string]int{}
			// Visit "." and first child dir, then simulate process crash/restart.
			for range 2 {
				d := stepIndexDir(t, w1)
				if d != "" {
					visitedBefore[d]++
				}
			}
			w1.discard()
			dbPath := s1.file
			testutil.FailErr(t, "drain before restart", c.Drain(context.Background()))

			// Restart: new Catalog pointing at same LYCAON_CONFIG_DIR / persisted SQLite frontier.
			c2 := New()
			s2, err := c2.Trees.indexStore(t.Context(), "p", root)
			testutil.FailErr(t, "reopen store", err)
			if s2.file != dbPath {
				t.Fatalf("K03[%d] restarted store used different db path", idx)
			}
			release2, err := s2.writer.Write(t.Context(), s2.writable)
			testutil.FailErr(t, "writer gate 2", err)
			s2.mu.Lock()
			err = loadStore(t.Context(), s2)
			s2.mu.Unlock()
			release2()
			testutil.FailErr(t, "loadStore 2", err)
			db2, err := openTreeDB(t.Context(), s2.file)
			testutil.FailErr(t, "openTreeDB 2", err)
			osRoot2, err := os.OpenRoot(root.Path)
			testutil.FailErr(t, "openRoot 2", err)
			w2 := &indexWalk{store: s2, db: db2, root: osRoot2, budget: indexBudget{limits: s2.policy.budgets}}
			testutil.FailErr(t, "resume prepare", w2.prepare(t.Context(), false, nil))
			for {
				d := stepIndexDir(t, w2)
				if d == "" {
					break
				}
				if visitedBefore[d] > 0 {
					t.Fatalf("K03[%d] mid-walk restart re-read finished dir %q", idx, d)
				}
			}
			w2.discard()
			_ = osRoot2.Close()
			_ = db2.Close()
			testutil.FailErr(t, "drain c2", c2.Drain(context.Background()))

		case 2:
			// Paging / navigation across a generation change never crosses generations.
			nDirs := 3 + rng.IntN(4)
			for d := range nDirs {
				writeIndexFile(t, rootDir, fmt.Sprintf("mod_%02d/a.go", d), "v1")
			}
			_, err := c.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
			testutil.FailErr(t, "observe root", err)
			head, err := c.Directories.OpenNavigation(t.Context(), "p", root)
			testutil.FailErr(t, "open nav", err)
			genBefore := head.Generation
			pin, err := head.Retain()
			testutil.FailErr(t, "retain gen", err)
			testutil.FailErr(t, "close head", head.Close())

			// Mutate tree and publish a new generation.
			writeIndexFile(t, rootDir, "mod_new/b.go", "v2")
			c.InvalidateRoot(root.Path, "mod_new/b.go")
			_, err = c.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
			testutil.FailErr(t, "observe root v2", err)

			// Pinned page read stays on genBefore and never sees mod_new.
			pinned, err := pin.OpenNavigation(t.Context())
			testutil.FailErr(t, "open pinned", err)
			if pinned.Generation != genBefore {
				t.Fatalf("K03[%d] pinned generation crossed from %d to %d", idx, genBefore, pinned.Generation)
			}
			if _, err := pinned.Entry(t.Context(), "mod_new"); err == nil {
				t.Fatalf("K03[%d] pinned generation leaked newer entry mod_new", idx)
			}
			testutil.FailErr(t, "close pinned", pinned.Close())
			pin.Release()
		}

		testutil.FailErr(t, "drain k03", c.Drain(context.Background()))
		_ = os.RemoveAll(rootDir)
		passed++
	}

	cf03LogSummary(t, cf03Summary{
		Family:        "K03",
		Lane:          "in-process",
		BaseSeed:      cf03BaseSeed,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		CaseSensitive: caseSens,
		Planned:       planned,
		Executed:      passed,
		Passed:        passed,
		Failed:        0,
		DeclaredGaps:  map[string]int{},
	})
}

// TestCF03_K04_TraversalSafetyAndNames runs 1,000 seeded in-process K04 instances covering:
// 1) ancestor symlink swap mid-walk verifying nothing outside root is read,
// 2) mid-walk permission flip,
// 3) hostile names: Unicode NFC/NFD, case-only diffs (D9 platform-aware), very long paths,
//    and newline/control characters.
func TestCF03_K04_TraversalSafetyAndNames(t *testing.T) {
	const planned = 1000
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)
	caseSens := cf03ProbeCaseSensitive(t, cfgDir)
	isRoot := os.Geteuid() == 0

	gapCounts := map[string]int{}
	passed := 0
	for idx := range planned {
		rng := cf03SeedRNG(cf03BaseSeed, "K04", idx)
		mode := idx % 4
		c := New()
		rootDir := t.TempDir()
		root := Root{ID: fmt.Sprintf("k04-%d", idx), Path: rootDir}

		switch mode {
		case 0:
			// Ancestor symlink swap mid-walk: outside sentinel must NEVER be read/indexed.
			outsideDir := t.TempDir()
			writeIndexFile(t, outsideDir, "canary_outside.txt", "SECRET_OUTSIDE")
			writeIndexFile(t, rootDir, "keep.txt", "inside")
			writeIndexFile(t, rootDir, "parent/child/inside.txt", "inside")

			s, w := indexWalkFixture(t, c, root)
			// Visit "." so "parent" is queued in frontier.
			_ = stepIndexDir(t, w)
			// Swap "parent" with a symlink pointing to outsideDir mid-walk!
			testutil.FailErr(t, "remove parent", os.RemoveAll(filepath.Join(rootDir, "parent")))
			testutil.FailErr(t, "swap symlink", os.Symlink(outsideDir, filepath.Join(rootDir, "parent")))
			for {
				d := stepIndexDir(t, w)
				if d == "" {
					break
				}
			}
			w.discard()
			r, err := openIndexReader(t, s)
			testutil.FailErr(t, "open swapped reader", err)
			paths := cf03AllIndexedPaths(t, r, FileScope{Audience: HumanAudience, IncludeHidden: true})
			for _, p := range paths {
				if strings.Contains(p, "canary_outside") {
					t.Fatalf("K04[%d] symlink swap escaped root and indexed %q", idx, p)
				}
			}
			testutil.FailErr(t, "close swapped reader", r.Close())
			_ = os.RemoveAll(outsideDir)

		case 1:
			// Mid-walk permission flip.
			writeIndexFile(t, rootDir, "good.txt", "ok")
			writeIndexFile(t, rootDir, "flip/inner.txt", "inner")
			s, w := indexWalkFixture(t, c, root)
			_ = stepIndexDir(t, w) // "." queues "flip"
			flipPath := filepath.Join(rootDir, "flip")
			if !isRoot {
				testutil.FailErr(t, "chmod 000 flip", os.Chmod(flipPath, 0o000))
			}
			for {
				d := stepIndexDir(t, w)
				if d == "" {
					break
				}
			}
			w.discard()
			if !isRoot {
				testutil.FailErr(t, "restore flip chmod", os.Chmod(flipPath, 0o755))
				r, err := openIndexReader(t, s)
				testutil.FailErr(t, "open flip reader", err)
				cov, err := r.Coverage(t.Context())
				testutil.FailErr(t, "flip coverage", err)
				if cov.FailedDirectories < 1 || cov.Exhaustive() {
					t.Fatalf("K04[%d] permission flip did not record fault: %+v", idx, cov)
				}
				testutil.FailErr(t, "close flip reader", r.Close())
				gapCounts["catalog_failed"]++
			}

		case 2:
			// Hostile Unicode NFC/NFD + case-only diffs (D9 platform-aware).
			nfc := "caf\u00e9_" + fmt.Sprintf("%d.txt", idx)
			nfd := "cafe\u0301_" + fmt.Sprintf("%d.txt", idx)
			writeIndexFile(t, rootDir, "u/"+nfc, "nfc")
			if caseSens {
				writeIndexFile(t, rootDir, "u/"+nfd, "nfd")
				writeIndexFile(t, rootDir, "case/Alpha.txt", "upper")
				writeIndexFile(t, rootDir, "case/alpha.txt", "lower")
			} else {
				writeIndexFile(t, rootDir, "case/Alpha.txt", "single")
			}
			r := waitIndex(t, c, root)
			got := cf03AllIndexedPaths(t, r, FileScope{Audience: HumanAudience, IncludeHidden: true})
			want := cf03RefWalkFiles(t, rootDir, map[string]bool{}, false)
			if !slices.Equal(got, want) {
				t.Fatalf("K04[%d] unicode/case mismatch: got=%v want=%v", idx, got, want)
			}
			testutil.FailErr(t, "close unicode reader", r.Close())

		case 3:
			// Very long paths and control/newline characters in filenames.
			longSeg := strings.Repeat("a", 40+rng.IntN(20))
			longPath := path.Join(longSeg, longSeg, longSeg, fmt.Sprintf("f_%d.txt", idx))
			writeIndexFile(t, rootDir, longPath, "long")
			ctrlName := fmt.Sprintf("ctrl\tspace %d.txt", idx)
			writeIndexFile(t, rootDir, "special/"+ctrlName, "ctrl")
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				nlName := fmt.Sprintf("nl\nname_%d.txt", idx)
				writeIndexFile(t, rootDir, "special/"+nlName, "nl")
			}
			r := waitIndex(t, c, root)
			got := cf03AllIndexedPaths(t, r, FileScope{Audience: HumanAudience, IncludeHidden: true})
			want := cf03RefWalkFiles(t, rootDir, map[string]bool{}, false)
			if !slices.Equal(got, want) {
				t.Fatalf("K04[%d] long/ctrl mismatch: got=%v want=%v", idx, got, want)
			}
			testutil.FailErr(t, "close long/ctrl reader", r.Close())
		}

		testutil.FailErr(t, "drain k04", c.Drain(context.Background()))
		_ = os.RemoveAll(rootDir)
		passed++
	}

	cf03LogSummary(t, cf03Summary{
		Family:        "K04",
		Lane:          "in-process",
		BaseSeed:      cf03BaseSeed,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		CaseSensitive: caseSens,
		Planned:       planned,
		Executed:      passed,
		Passed:        passed,
		Failed:        0,
		DeclaredGaps:  gapCounts,
	})
}
