package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

const (
	cf08DefaultBaseSeed = "20261127"
	cf08R01Instances    = 300
	cf08R02Instances    = 1000
	cf08R03Instances    = 3000
)

var cf08Tokens = []string{
	"alpha",
	"beta",
	"gamma",
	"delta",
	"catalog",
	"frontier",
	"watcher",
	"snapshot",
	"search_hit",
	"replace_target",
	"PaintedWolf",
	"order_42",
}

func cf08BaseSeed() string {
	if s := strings.TrimSpace(os.Getenv("CF08_SEED")); s != "" {
		return s
	}
	return cf08DefaultBaseSeed
}

func cf08DeriveSeed(baseSeed, familyID string, instanceIdx int) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", baseSeed, familyID, instanceIdx)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func cf08SHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type cf08FamilyReport struct {
	Family        string         `json:"family"`
	Lane          string         `json:"lane"`
	BaseSeed      string         `json:"base_seed"`
	Planned       int            `json:"planned"`
	Executed      int            `json:"executed"`
	Passed        int            `json:"passed"`
	Failed        int            `json:"failed"`
	Blocked       int            `json:"blocked"`
	Inconclusive  int            `json:"inconclusive"`
	EdgesPlanned  int            `json:"edges_planned"`
	EdgesExecuted int            `json:"edges_executed"`
	EdgesPassed   int            `json:"edges_passed"`
	EdgesFailed   int            `json:"edges_failed"`
	DeclaredGaps  map[string]int `json:"declared_gaps"`
	RGVersion     string         `json:"rg_version,omitempty"`
	Failures      []string       `json:"failures,omitempty"`
}

func cf08RecordFamilyReport(t *testing.T, rep cf08FamilyReport) {
	t.Helper()
	summaryDir := strings.TrimSpace(os.Getenv("CF08_SUMMARY_DIR"))
	if summaryDir == "" {
		return
	}
	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		t.Logf("cf08 summary mkdir failed: %v", err)
		return
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Logf("cf08 summary marshal failed: %v", err)
		return
	}
	outPath := filepath.Join(summaryDir, fmt.Sprintf("inprocess_%s.json", strings.ToLower(rep.Family)))
	if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
		t.Logf("cf08 summary write failed: %v", err)
	}
}

type cf08ReplaceStore struct {
	project *project.Project
}

func (s cf08ReplaceStore) ReadReplaceContent(rootID, path string) (ReplaceContentRead, error) {
	read, err := projectsource.ReadProjectSource(s.project, projectsource.SourceReadRequest{
		Path: path, RootID: rootID,
	})
	if err != nil {
		switch {
		case errors.Is(err, projectsource.ErrSourceWriteConflict):
			return ReplaceContentRead{}, ErrReplaceWriteConflict
		case errors.Is(err, projectsource.ErrSourceWriteTooLarge):
			return ReplaceContentRead{}, ErrReplaceTooLarge
		case errors.Is(err, projectsource.ErrSourceBinary):
			return ReplaceContentRead{}, ErrReplaceBinary
		case errors.Is(err, projectsource.ErrSourceNotFound):
			return ReplaceContentRead{}, ErrReplaceNotFound
		case errors.Is(err, projectsource.ErrSourcePathDenied):
			return ReplaceContentRead{}, ErrReplacePathDenied
		default:
			return ReplaceContentRead{}, err
		}
	}
	return ReplaceContentRead{
		Content: read.Content, SHA256: read.SHA256, Encoding: read.Encoding,
		Truncated: read.OverLimit || read.Binary,
	}, nil
}

// =============================================================================
// R01 · Preview states (300 in-process instances + 4 hand-written edges)
// =============================================================================

func TestCF08_R01_PreviewStates(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	baseSeed := cf08BaseSeed()
	rep := cf08FamilyReport{
		Family:       "R01",
		Lane:         "in_process",
		BaseSeed:     baseSeed,
		Planned:      cf08R01Instances,
		EdgesPlanned: 4,
		DeclaredGaps: map[string]int{},
	}

	catalog := sourcecatalog.New()
	t.Cleanup(func() { _ = catalog.Drain(context.Background()) })

	boundedCatalog := sourcecatalog.New()
	boundedCatalog.SetScopes(boundedPreviewScope{})
	t.Cleanup(func() { _ = boundedCatalog.Drain(context.Background()) })

	states := []ReplacePreviewState{ReplacePreviewPreparing, ReplacePreviewReady, ReplacePreviewLimited}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)

	for i := 0; i < cf08R01Instances; i++ {
		i := i
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rng := rand.New(rand.NewSource(cf08DeriveSeed(baseSeed, "R01", i)))
			targetState := states[i%len(states)]
			tok := cf08Tokens[rng.Intn(len(cf08Tokens))]
			repl := tok + "_new"

			var failMsg string
			var gaps []string

			switch targetState {
			case ReplacePreviewReady:
				dir := t.TempDir()
				fileCount := 1 + rng.Intn(4)
				expectMatches := i%7 != 0
				for f := 0; f < fileCount; f++ {
					body := fmt.Sprintf("line_%d: unrelated_word_%d\n", f, f)
					if expectMatches {
						body = fmt.Sprintf("line_%d: %s_%d\n", f, tok, f)
					}
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f_%02d.txt", f)), []byte(body), 0o644); err != nil {
						failMsg = fmt.Sprintf("R01[%d] write: %v", i, err)
						break
					}
				}
				if failMsg == "" {
					res, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{
						Query:       TextExpr{Text: tok},
						Replacement: repl,
						Roots:       []CodeRoot{{ProjectID: fmt.Sprintf("p-r01-%d", i), RootID: fmt.Sprintf("r-r01-%d", i), Path: dir}},
					})
					if err != nil {
						failMsg = fmt.Sprintf("R01[%d] ready err: %v", i, err)
					} else {
						wantFiles := 0
						if expectMatches {
							wantFiles = fileCount
						}
						if res.State != ReplacePreviewReady || len(res.Issues) != 0 || res.Truncated || len(res.Files) != wantFiles {
							failMsg = fmt.Sprintf("R01[%d] ready mismatch: state=%s issues=%v files=%d want=%d", i, res.State, res.Issues, len(res.Files), wantFiles)
						}
					}
				}

			case ReplacePreviewPreparing:
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "prep.txt"), []byte(fmt.Sprintf("%s_val\n", tok)), 0o644); err != nil {
					failMsg = fmt.Sprintf("R01[%d] write: %v", i, err)
					break
				}
				req := ReplacePreviewRequest{
					Query:       TextExpr{Text: tok},
					Replacement: repl,
					Roots:       []CodeRoot{{ProjectID: fmt.Sprintf("p-r01-%d", i), RootID: fmt.Sprintf("r-r01-%d", i), Path: dir}},
				}
				if (i/3)%2 == 0 {
					// Refreshing readable generation -> catalog_refreshing -> preparing immediately.
					localCatalog := sourcecatalog.New()
					if _, err := awaitReplacePreview(t.Context(), localCatalog, req); err != nil {
						_ = localCatalog.Drain(context.Background())
						failMsg = fmt.Sprintf("R01[%d] settle before refresh: %v", i, err)
						break
					}
					release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
						Lane: dir, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
					})
					if err != nil {
						_ = localCatalog.Drain(context.Background())
						failMsg = fmt.Sprintf("R01[%d] acquire refresh barrier: %v", i, err)
						break
					}
					_ = os.WriteFile(filepath.Join(dir, "late.txt"), []byte(fmt.Sprintf("%s_late\n", tok)), 0o644)
					localCatalog.InvalidateRoot(dir, "late.txt")
					res, err := previewReplace(t.Context(), localCatalog, req)
					release()
					_ = localCatalog.Drain(context.Background())
					if err != nil {
						failMsg = fmt.Sprintf("R01[%d] preparing refresh err: %v", i, err)
					} else {
						for _, iss := range res.Issues {
							gaps = append(gaps, string(iss.Reason))
						}
						if res.State != ReplacePreviewPreparing || len(res.Files) != 0 || len(res.Issues) == 0 {
							failMsg = fmt.Sprintf("R01[%d] preparing refresh mismatch: state=%s files=%d issues=%v", i, res.State, len(res.Files), res.Issues)
						}
					}
				} else {
					// Cold warming root -> catalog_warming -> preparing.
					release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
						Lane: dir, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
					})
					if err != nil {
						failMsg = fmt.Sprintf("R01[%d] acquire cold barrier: %v", i, err)
						break
					}
					res, err := previewReplace(t.Context(), catalog, req)
					release()
					if err != nil {
						failMsg = fmt.Sprintf("R01[%d] preparing cold err: %v", i, err)
					} else {
						for _, iss := range res.Issues {
							gaps = append(gaps, string(iss.Reason))
						}
						if res.State != ReplacePreviewPreparing || len(res.Files) != 0 || len(res.Issues) == 0 {
							failMsg = fmt.Sprintf("R01[%d] preparing cold mismatch: state=%s files=%d issues=%v", i, res.State, len(res.Files), res.Issues)
						}
					}
				}

			case ReplacePreviewLimited:
				dir := t.TempDir()
				if i%2 == 0 {
					if err := os.Mkdir(filepath.Join(dir, "wide"), 0o755); err != nil {
						failMsg = fmt.Sprintf("R01[%d] mkdir: %v", i, err)
						break
					}
					for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
						if err := os.WriteFile(filepath.Join(dir, "wide", name), []byte(tok+"\n"), 0o644); err != nil {
							failMsg = fmt.Sprintf("R01[%d] write wide: %v", i, err)
							break
						}
					}
					if failMsg == "" {
						_ = os.WriteFile(filepath.Join(dir, "ok.txt"), []byte(tok+"\n"), 0o644)
						res, err := awaitReplacePreview(t.Context(), boundedCatalog, ReplacePreviewRequest{
							Query:       TextExpr{Text: tok},
							Replacement: repl,
							Roots:       []CodeRoot{{ProjectID: fmt.Sprintf("p-r01-b-%d", i), RootID: fmt.Sprintf("r-r01-b-%d", i), Path: dir}},
						})
						if err != nil {
							failMsg = fmt.Sprintf("R01[%d] limited bounded err: %v", i, err)
						} else {
							for _, iss := range res.Issues {
								gaps = append(gaps, string(iss.Reason))
							}
							if res.State != ReplacePreviewLimited || len(res.Files) != 1 || !hasCoverageIssue(res.Issues, IssueCatalogBounded) {
								failMsg = fmt.Sprintf("R01[%d] limited bounded mismatch: state=%s files=%d issues=%v", i, res.State, len(res.Files), res.Issues)
							}
						}
					}
				} else {
					_ = os.WriteFile(filepath.Join(dir, "healthy.txt"), []byte(tok+"\n"), 0o644)
					roots := []CodeRoot{
						{ProjectID: fmt.Sprintf("p-r01-e-%d", i), RootID: "bad", Path: filepath.Join(t.TempDir(), "missing")},
						{ProjectID: fmt.Sprintf("p-r01-e-%d", i), RootID: "good", Path: dir},
					}
					res, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{
						Query:       TextExpr{Text: tok},
						Replacement: repl,
						Roots:       roots,
					})
					if err != nil {
						failMsg = fmt.Sprintf("R01[%d] limited absent-root err: %v", i, err)
					} else {
						for _, iss := range res.Issues {
							gaps = append(gaps, string(iss.Reason))
						}
						if res.State != ReplacePreviewLimited || len(res.Files) != 1 || res.Files[0].RootID != "good" || !hasCoverageIssue(res.Issues, IssueExecutorError) {
							failMsg = fmt.Sprintf("R01[%d] limited absent-root mismatch: state=%s files=%d issues=%v", i, res.State, len(res.Files), res.Issues)
						}
					}
				}
			}

			mu.Lock()
			rep.Executed++
			for _, g := range gaps {
				rep.DeclaredGaps[g]++
			}
			if failMsg != "" {
				rep.Failed++
				rep.Failures = append(rep.Failures, failMsg)
			} else {
				rep.Passed++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 4 Hand-written edges for R01.
	runEdge := func(name string, fn func() error) {
		rep.EdgesExecuted++
		if err := fn(); err != nil {
			rep.EdgesFailed++
			rep.Failures = append(rep.Failures, fmt.Sprintf("%s: %v", name, err))
		} else {
			rep.EdgesPassed++
		}
	}

	// R01-E01: Empty ready preview -> only state where "No replaceable matches" is valid.
	runEdge("R01-E01", func() error {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world\n"), 0o644); err != nil {
			return err
		}
		res, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{
			Query: TextExpr{Text: "zz_no_such_token_zz"}, Replacement: "x",
			Roots: []CodeRoot{{ProjectID: "p-e1", RootID: "r-e1", Path: dir}},
		})
		if err != nil {
			return err
		}
		if res.State != ReplacePreviewReady || len(res.Files) != 0 || len(res.Issues) != 0 {
			return fmt.Errorf("expected empty ready preview, got %+v", res)
		}
		return nil
	})

	// R01-E02: Empty preparing preview must NOT appear ready.
	runEdge("R01-E02", func() error {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world\n"), 0o644); err != nil {
			return err
		}
		release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
			Lane: dir, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
		})
		if err != nil {
			return err
		}
		res, err := previewReplace(t.Context(), catalog, ReplacePreviewRequest{
			Query: TextExpr{Text: "zz_no_such_token_zz"}, Replacement: "x",
			Roots: []CodeRoot{{ProjectID: "p-e2", RootID: "r-e2", Path: dir}},
		})
		release()
		if err != nil {
			return err
		}
		if res.State != ReplacePreviewPreparing || len(res.Files) != 0 || len(res.Issues) == 0 {
			return fmt.Errorf("expected empty preparing preview with issues, got %+v", res)
		}
		return nil
	})

	// R01-E03: Limited preview disables Apply all while keeping selected files applicable.
	runEdge("R01-E03", func() error {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "good.txt"), []byte("alpha line\n"), 0o644); err != nil {
			return err
		}
		p, err := projecttest.CreateWithRoot(t.Context(), projecttest.NewRegistry(t), dir)
		if err != nil {
			return err
		}
		roots := []CodeRoot{
			{ProjectID: p.ID, RootID: "missing-root", Path: filepath.Join(t.TempDir(), "absent")},
			{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: dir},
		}
		res, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{
			Query: TextExpr{Text: "alpha"}, Replacement: "beta", Roots: roots,
		})
		if err != nil {
			return err
		}
		if res.State != ReplacePreviewLimited || len(res.Files) != 1 {
			return fmt.Errorf("expected limited with 1 file, got %+v", res)
		}
		plan, err := PlanReplace(ReplacePlanRequest{
			Query: TextExpr{Text: "alpha"}, Replacement: "beta",
			Store: testReplaceStore{project: p},
			Files: []ReplaceApplyFile{{
				RootID: res.Files[0].RootID, Path: res.Files[0].Path,
				SHA256: res.Files[0].SHA256, HunkIndexes: []int{0},
			}},
		})
		if err != nil {
			return err
		}
		if len(plan.Files) != 1 || !plan.Files[0].Applied || len(plan.Writes) != 1 || string(plan.Writes[0].After) != "beta line\n" {
			return fmt.Errorf("selected file from limited preview failed to plan: %+v", plan)
		}
		return nil
	})

	// R01-E04: Changing inputs / canceling during preparation never publishes older hunks.
	runEdge("R01-E04", func() error {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "swap.txt"), []byte("alpha and beta\n"), 0o644); err != nil {
			return err
		}
		release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
			Lane: dir, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
		})
		if err != nil {
			return err
		}
		ctx1, cancel1 := context.WithCancel(t.Context())
		cancel1()
		_, err1 := previewReplace(ctx1, catalog, ReplacePreviewRequest{
			Query: TextExpr{Text: "alpha"}, Replacement: "OLD_REPL",
			Roots: []CodeRoot{{ProjectID: "p-e4", RootID: "r-e4", Path: dir}},
		})
		release()
		if !errors.Is(err1, context.Canceled) {
			return fmt.Errorf("canceled request should return context.Canceled, got %v", err1)
		}
		res2, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{
			Query: TextExpr{Text: "beta"}, Replacement: "NEW_REPL",
			Roots: []CodeRoot{{ProjectID: "p-e4", RootID: "r-e4", Path: dir}},
		})
		if err != nil {
			return err
		}
		if res2.State != ReplacePreviewReady || len(res2.Files) != 1 || len(res2.Files[0].Hunks) != 1 || !strings.Contains(res2.Files[0].Hunks[0].After, "NEW_REPL") || strings.Contains(res2.Files[0].Hunks[0].After, "OLD_REPL") {
			return fmt.Errorf("unexpected hunks after input change: %+v", res2)
		}
		return nil
	})

	cf08RecordFamilyReport(t, rep)
	if rep.Failed > 0 || rep.EdgesFailed > 0 {
		t.Fatalf("CF08 R01 failures (%d inst, %d edges): %v", rep.Failed, rep.EdgesFailed, rep.Failures[:min(5, len(rep.Failures))])
	}
}

// =============================================================================
// R02 · Revision guard (1,000 in-process instances + 6 hand-written edges)
// =============================================================================

func cf08RunR02Case(t *testing.T, dir string, files map[string][]byte, pattern, replacement string, drifts []func(dir string) (drifted map[string]string, deleted map[string]bool, created map[string]string)) error {
	t.Helper()
	for rel, raw := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, raw, 0o644); err != nil {
			return err
		}
	}
	p := &project.Project{
		ID:    "p-r02",
		Roots: []project.Root{{ID: "r-r02", Path: dir}},
	}
	rootID := p.Roots[0].ID

	// Build preview hunks + SHA-256 directly from pre-drift bytes.
	matcher, err := compileReplaceMatcher(TextExpr{Text: pattern}, MatchFlags{})
	if err != nil {
		return err
	}
	var applyFiles []ReplaceApplyFile
	expectedAfter := map[string]string{}
	sortedPaths := make([]string, 0, len(files))
	for rel := range files {
		sortedPaths = append(sortedPaths, rel)
	}
	sort.Strings(sortedPaths)
	for _, rel := range sortedPaths {
		raw := files[rel]
		hunks := collectReplaceHunks(string(raw), matcher, replacement)
		if len(hunks) == 0 {
			continue
		}
		idxs := make([]int, len(hunks))
		for k := range hunks {
			idxs[k] = k
		}
		next, ok := applyHunksToContent(string(raw), hunks)
		if !ok {
			return fmt.Errorf("applyHunksToContent failed for %s", rel)
		}
		expectedAfter[rel] = cf08SHA256([]byte(next))
		applyFiles = append(applyFiles, ReplaceApplyFile{
			RootID: rootID, Path: rel, SHA256: cf08SHA256(raw), HunkIndexes: idxs,
		})
	}

	driftedSHA := map[string]string{}
	deletedSet := map[string]bool{}
	createdSHA := map[string]string{}
	for _, fn := range drifts {
		d, del, c := fn(dir)
		for k, v := range d {
			driftedSHA[k] = v
		}
		for k, v := range del {
			deletedSet[k] = v
		}
		for k, v := range c {
			createdSHA[k] = v
		}
	}

	plan, err := PlanReplace(ReplacePlanRequest{
		Query: TextExpr{Text: pattern}, Replacement: replacement,
		Store: cf08ReplaceStore{project: p}, Files: applyFiles,
	})
	if err != nil {
		return fmt.Errorf("PlanReplace: %w", err)
	}
	// Commit planned writes to disk (only unaffected files are in plan.Writes).
	for _, w := range plan.Writes {
		abs := filepath.Join(dir, filepath.FromSlash(w.Path))
		if err := os.WriteFile(abs, w.After, 0o644); err != nil {
			return err
		}
	}

	for _, oc := range plan.Files {
		abs := filepath.Join(dir, filepath.FromSlash(oc.Path))
		if deletedSet[oc.Path] {
			if oc.Applied || !oc.Skipped || oc.Reason != "source_not_found" {
				return fmt.Errorf("deleted file %s outcome=%+v", oc.Path, oc)
			}
			if _, statErr := os.Stat(abs); !os.IsNotExist(statErr) {
				return fmt.Errorf("deleted file %s reappeared on disk", oc.Path)
			}
			continue
		}
		if wantDrifted, isDrifted := driftedSHA[oc.Path]; isDrifted {
			if oc.Applied || !oc.Skipped || oc.Reason != "source_write_conflict" {
				return fmt.Errorf("drifted file %s was not refused: outcome=%+v", oc.Path, oc)
			}
			diskBytes, readErr := os.ReadFile(abs)
			if readErr != nil {
				return fmt.Errorf("read drifted %s: %w", oc.Path, readErr)
			}
			if got := cf08SHA256(diskBytes); got != wantDrifted {
				return fmt.Errorf("drifted file %s mutated: got=%s want=%s", oc.Path, got, wantDrifted)
			}
			continue
		}
		// Unaffected file must apply and match expectedAfter.
		if !oc.Applied || oc.Skipped {
			return fmt.Errorf("unaffected file %s did not apply: outcome=%+v", oc.Path, oc)
		}
		diskBytes, readErr := os.ReadFile(abs)
		if readErr != nil {
			return fmt.Errorf("read applied %s: %w", oc.Path, readErr)
		}
		if got := cf08SHA256(diskBytes); got != expectedAfter[oc.Path] {
			return fmt.Errorf("applied file %s hash mismatch: got=%s want=%s", oc.Path, got, expectedAfter[oc.Path])
		}
	}

	// Post-preview created files must be completely untouched.
	for rel, wantSHA := range createdSHA {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		diskBytes, readErr := os.ReadFile(abs)
		if readErr != nil {
			return fmt.Errorf("post-preview file %s missing: %w", rel, readErr)
		}
		if got := cf08SHA256(diskBytes); got != wantSHA {
			return fmt.Errorf("post-preview file %s mutated: got=%s want=%s", rel, got, wantSHA)
		}
	}
	return nil
}

func TestCF08_R02_RevisionGuard(t *testing.T) {
	baseSeed := cf08BaseSeed()
	rep := cf08FamilyReport{
		Family:       "R02",
		Lane:         "in_process",
		BaseSeed:     baseSeed,
		Planned:      cf08R02Instances,
		EdgesPlanned: 6,
		DeclaredGaps: map[string]int{},
	}

	writerKinds := []string{"person", "external", "agent"}

	for i := 0; i < cf08R02Instances; i++ {
		rng := rand.New(rand.NewSource(cf08DeriveSeed(baseSeed, "R02", i)))
		rep.Executed++
		tok := cf08Tokens[rng.Intn(len(cf08Tokens))]
		repl := tok + "_applied"
		driftCount := 1 + rng.Intn(20) // 1..20 drifts per ledger.md
		fileCount := max(4, min(24, driftCount+2))

		files := make(map[string][]byte, fileCount)
		paths := make([]string, 0, fileCount)
		for f := 0; f < fileCount; f++ {
			rel := fmt.Sprintf("pkg/file_%02d.txt", f)
			paths = append(paths, rel)
			files[rel] = []byte(fmt.Sprintf("line 1: %s_a\nline 2: %s_b\n", tok, tok))
		}

		driftFn := func(dir string) (map[string]string, map[string]bool, map[string]string) {
			drifted := map[string]string{}
			deleted := map[string]bool{}
			created := map[string]string{}
			for d := 0; d < driftCount; d++ {
				writer := writerKinds[rng.Intn(len(writerKinds))]
				action := rng.Intn(4) // 0=edit, 1=delete, 2=delete+recreate, 3=create_new
				target := paths[rng.Intn(len(paths)-1)]
				abs := filepath.Join(dir, filepath.FromSlash(target))
				switch action {
				case 0:
					if deleted[target] {
						continue
					}
					newBody := []byte(fmt.Sprintf("drifted by %s step %d: %s\n", writer, d, tok))
					_ = os.WriteFile(abs, newBody, 0o644)
					drifted[target] = cf08SHA256(newBody)
				case 1:
					_ = os.Remove(abs)
					delete(drifted, target)
					deleted[target] = true
				case 2:
					_ = os.Remove(abs)
					recreated := []byte(fmt.Sprintf("recreated by %s step %d: %s\n", writer, d, tok))
					_ = os.WriteFile(abs, recreated, 0o644)
					delete(deleted, target)
					drifted[target] = cf08SHA256(recreated)
				case 3:
					newRel := fmt.Sprintf("pkg/post_preview_%02d.txt", d)
					newBody := []byte(fmt.Sprintf("created after preview by %s: %s\n", writer, tok))
					_ = os.WriteFile(filepath.Join(dir, filepath.FromSlash(newRel)), newBody, 0o644)
					created[newRel] = cf08SHA256(newBody)
				}
			}
			return drifted, deleted, created
		}

		if err := cf08RunR02Case(t, t.TempDir(), files, tok, repl, []func(string) (map[string]string, map[string]bool, map[string]string){driftFn}); err != nil {
			rep.Failed++
			rep.Failures = append(rep.Failures, fmt.Sprintf("R02[%d]: %v", i, err))
		} else {
			rep.Passed++
		}
	}

	// 6 Hand-written edges for R02.
	runEdge := func(name string, fn func() error) {
		rep.EdgesExecuted++
		if err := fn(); err != nil {
			rep.EdgesFailed++
			rep.Failures = append(rep.Failures, fmt.Sprintf("%s: %v", name, err))
		} else {
			rep.EdgesPassed++
		}
	}

	// R02-E01: Single-byte change between preview and apply.
	runEdge("R02-E01", func() error {
		return cf08RunR02Case(t, t.TempDir(), map[string][]byte{"a.txt": []byte("alpha 1\n")}, "alpha", "beta", []func(string) (map[string]string, map[string]bool, map[string]string){
			func(dir string) (map[string]string, map[string]bool, map[string]string) {
				b := []byte("alpha 2\n")
				_ = os.WriteFile(filepath.Join(dir, "a.txt"), b, 0o644)
				return map[string]string{"a.txt": cf08SHA256(b)}, nil, nil
			},
		})
	})

	// R02-E02: File deleted between preview and apply.
	runEdge("R02-E02", func() error {
		return cf08RunR02Case(t, t.TempDir(), map[string][]byte{"a.txt": []byte("alpha\n"), "b.txt": []byte("alpha\n")}, "alpha", "beta", []func(string) (map[string]string, map[string]bool, map[string]string){
			func(dir string) (map[string]string, map[string]bool, map[string]string) {
				_ = os.Remove(filepath.Join(dir, "a.txt"))
				return nil, map[string]bool{"a.txt": true}, nil
			},
		})
	})

	// R02-E03: File deleted and recreated with different bytes.
	runEdge("R02-E03", func() error {
		return cf08RunR02Case(t, t.TempDir(), map[string][]byte{"a.txt": []byte("alpha old\n")}, "alpha", "beta", []func(string) (map[string]string, map[string]bool, map[string]string){
			func(dir string) (map[string]string, map[string]bool, map[string]string) {
				_ = os.Remove(filepath.Join(dir, "a.txt"))
				b := []byte("alpha recreated\n")
				_ = os.WriteFile(filepath.Join(dir, "a.txt"), b, 0o644)
				return map[string]string{"a.txt": cf08SHA256(b)}, nil, nil
			},
		})
	})

	// R02-E04: File deleted and recreated with identical bytes -> SHA matches, applies cleanly.
	runEdge("R02-E04", func() error {
		orig := []byte("alpha same\n")
		return cf08RunR02Case(t, t.TempDir(), map[string][]byte{"a.txt": orig}, "alpha", "beta", []func(string) (map[string]string, map[string]bool, map[string]string){
			func(dir string) (map[string]string, map[string]bool, map[string]string) {
				_ = os.Remove(filepath.Join(dir, "a.txt"))
				_ = os.WriteFile(filepath.Join(dir, "a.txt"), orig, 0o644)
				return nil, nil, nil
			},
		})
	})

	// R02-E05: Mixed batch (drifted + deleted + healthy + post-preview new file).
	runEdge("R02-E05", func() error {
		return cf08RunR02Case(t, t.TempDir(), map[string][]byte{
			"drifted.txt": []byte("alpha 1\n"),
			"deleted.txt": []byte("alpha 2\n"),
			"healthy.txt": []byte("alpha 3\n"),
		}, "alpha", "beta", []func(string) (map[string]string, map[string]bool, map[string]string){
			func(dir string) (map[string]string, map[string]bool, map[string]string) {
				d := []byte("alpha changed\n")
				_ = os.WriteFile(filepath.Join(dir, "drifted.txt"), d, 0o644)
				_ = os.Remove(filepath.Join(dir, "deleted.txt"))
				c := []byte("alpha brand new\n")
				_ = os.WriteFile(filepath.Join(dir, "created.txt"), c, 0o644)
				return map[string]string{"drifted.txt": cf08SHA256(d)}, map[string]bool{"deleted.txt": true}, map[string]string{"created.txt": cf08SHA256(c)}
			},
		})
	})

	// R02-E06: Same-length whitespace/case drift -> SHA catches it.
	runEdge("R02-E06", func() error {
		return cf08RunR02Case(t, t.TempDir(), map[string][]byte{"a.txt": []byte("alpha  1\n")}, "alpha", "beta", []func(string) (map[string]string, map[string]bool, map[string]string){
			func(dir string) (map[string]string, map[string]bool, map[string]string) {
				b := []byte("ALPHA  1\n")
				_ = os.WriteFile(filepath.Join(dir, "a.txt"), b, 0o644)
				return map[string]string{"a.txt": cf08SHA256(b)}, nil, nil
			},
		})
	})

	cf08RecordFamilyReport(t, rep)
	if rep.Failed > 0 || rep.EdgesFailed > 0 {
		t.Fatalf("CF08 R02 failures (%d inst, %d edges): %v", rep.Failed, rep.EdgesFailed, rep.Failures[:min(5, len(rep.Failures))])
	}
}

// =============================================================================
// R03 · Replacement differential (3,000 in-process instances + 6 edges vs rg 15.2.0)
// =============================================================================

type cf08RGEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type cf08RGMatchData struct {
	Path struct {
		Text string `json:"text"`
	} `json:"path"`
	AbsoluteOffset int `json:"absolute_offset"`
	Submatches     []struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"submatches"`
}

type cf08RGSpan struct {
	start int
	end   int
}

func cf08CaptureRGVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("rg", "--version").Output()
	if err != nil {
		t.Fatalf("ripgrep (rg) required for D3 R03 reference: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func cf08EncodeWithBOM(text, enc string) []byte {
	switch enc {
	case "utf-8":
		return []byte(text)
	case "utf-8-bom":
		return append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...)
	case "utf-16-le-bom":
		u := utf16.Encode([]rune(text))
		out := make([]byte, 2+len(u)*2)
		out[0], out[1] = 0xFF, 0xFE
		for i, v := range u {
			binary.LittleEndian.PutUint16(out[2+i*2:], v)
		}
		return out
	case "utf-16-be-bom":
		u := utf16.Encode([]rune(text))
		out := make([]byte, 2+len(u)*2)
		out[0], out[1] = 0xFE, 0xFF
		for i, v := range u {
			binary.BigEndian.PutUint16(out[2+i*2:], v)
		}
		return out
	default:
		return []byte(text)
	}
}

func cf08DecodeWithBOM(raw []byte) (enc string, bom []byte, text string, err error) {
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return "utf-8-bom", []byte{0xEF, 0xBB, 0xBF}, string(raw[3:]), nil
	}
	if bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) {
		body := raw[2:]
		if len(body)%2 != 0 {
			return "", nil, "", fmt.Errorf("odd utf-16le length")
		}
		u := make([]uint16, len(body)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(body[i*2:])
		}
		return "utf-16-le-bom", []byte{0xFF, 0xFE}, string(utf16.Decode(u)), nil
	}
	if bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		body := raw[2:]
		if len(body)%2 != 0 {
			return "", nil, "", fmt.Errorf("odd utf-16be length")
		}
		u := make([]uint16, len(body)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(body[i*2:])
		}
		return "utf-16-be-bom", []byte{0xFE, 0xFF}, string(utf16.Decode(u)), nil
	}
	if !utf8.Valid(raw) {
		return "", nil, "", fmt.Errorf("invalid utf-8")
	}
	return "utf-8", nil, string(raw), nil
}

// Independent D3 reference replacement using external ripgrep 15.2.0 --json + byte splicing.
func cf08ReferenceReplaceRG(rootDir, pattern, replacement string, flags MatchFlags) (map[string]string, error) {
	args := []string{"--json", "--no-ignore", "--color", "never", "--sort", "path"}
	if !flags.Regex {
		args = append(args, "--fixed-strings")
	}
	if flags.CaseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	if flags.WholeWord {
		args = append(args, "--word-regexp")
	}
	args = append(args, "--", pattern, ".")
	cmd := exec.Command("rg", args...)
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("rg failed: %w", err)
		}
	}

	spansByFile := map[string][]cf08RGSpan{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev cf08RGEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "match" {
			continue
		}
		var md cf08RGMatchData
		if json.Unmarshal(ev.Data, &md) != nil {
			continue
		}
		rel := strings.TrimPrefix(md.Path.Text, "./")
		for _, sub := range md.Submatches {
			spansByFile[rel] = append(spansByFile[rel], cf08RGSpan{
				start: md.AbsoluteOffset + sub.Start,
				end:   md.AbsoluteOffset + sub.End,
			})
		}
	}

	var re *regexp.Regexp
	if flags.Regex {
		expr := pattern
		if !flags.CaseSensitive {
			expr = "(?i)" + expr
		}
		var compErr error
		re, compErr = regexp.Compile(expr)
		if compErr != nil {
			return nil, compErr
		}
	}

	resultSHAs := map[string]string{}
	for rel, spans := range spansByFile {
		raw, readErr := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(rel)))
		if readErr != nil {
			return nil, readErr
		}
		enc, _, decoded, decErr := cf08DecodeWithBOM(raw)
		if decErr != nil {
			return nil, decErr
		}
		utf8Bytes := []byte(decoded)
		sort.Slice(spans, func(i, j int) bool {
			if spans[i].start != spans[j].start {
				return spans[i].start < spans[j].start
			}
			return spans[i].end < spans[j].end
		})
		var buf bytes.Buffer
		cursor := 0
		for _, sp := range spans {
			if sp.start < cursor || sp.end > len(utf8Bytes) {
				continue
			}
			buf.Write(utf8Bytes[cursor:sp.start])
			matched := utf8Bytes[sp.start:sp.end]
			if re != nil {
				sub := re.FindSubmatchIndex(matched)
				if sub != nil {
					buf.Write(re.Expand(nil, []byte(replacement), matched, sub))
				} else {
					buf.WriteString(replacement)
				}
			} else {
				buf.WriteString(replacement)
			}
			cursor = sp.end
		}
		buf.Write(utf8Bytes[cursor:])
		reencoded := cf08EncodeWithBOM(buf.String(), enc)
		resultSHAs[rel] = cf08SHA256(reencoded)
	}
	return resultSHAs, nil
}

func cf08RunR03Case(dir string, files map[string][]byte, pattern, replacement string, flags MatchFlags) error {
	for rel, raw := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, raw, 0o644); err != nil {
			return err
		}
	}

	refSHAs, err := cf08ReferenceReplaceRG(dir, pattern, replacement, flags)
	if err != nil {
		return fmt.Errorf("reference rg: %w", err)
	}

	p := &project.Project{
		ID:    "p-r03",
		Roots: []project.Root{{ID: "r-r03", Path: dir}},
	}
	store := testReplaceStore{project: p}
	matcher, err := compileReplaceMatcher(TextExpr{Text: pattern}, flags)
	if err != nil {
		return fmt.Errorf("compileReplaceMatcher: %w", err)
	}

	var applyFiles []ReplaceApplyFile
	sortedPaths := make([]string, 0, len(files))
	for rel := range files {
		sortedPaths = append(sortedPaths, rel)
	}
	sort.Strings(sortedPaths)

	for _, rel := range sortedPaths {
		read, readErr := store.ReadReplaceContent("r-r03", rel)
		if readErr != nil {
			return fmt.Errorf("ReadReplaceContent(%s): %w", rel, readErr)
		}
		hunks := collectReplaceHunks(read.Content, matcher, replacement)
		if len(hunks) == 0 {
			continue
		}
		idxs := make([]int, len(hunks))
		for k := range hunks {
			idxs[k] = k
		}
		applyFiles = append(applyFiles, ReplaceApplyFile{
			RootID: "r-r03", Path: rel, SHA256: read.SHA256, HunkIndexes: idxs,
		})
	}

	plan, err := PlanReplace(ReplacePlanRequest{
		Query: TextExpr{Text: pattern}, Replacement: replacement, Flags: flags,
		Store: store, Files: applyFiles,
	})
	if err != nil {
		return fmt.Errorf("PlanReplace: %w", err)
	}

	prodAfter := map[string]string{}
	for _, w := range plan.Writes {
		prodAfter[w.Path] = cf08SHA256(w.After)
	}

	// Compare product after-hashes with independent ripgrep 15.2.0 reference.
	for rel, refSHA := range refSHAs {
		gotSHA, ok := prodAfter[rel]
		if !ok {
			return fmt.Errorf("product missed file %s matched by rg reference", rel)
		}
		if gotSHA != refSHA {
			return fmt.Errorf("byte mismatch on %s: product=%s ref=%s", rel, gotSHA, refSHA)
		}
	}
	for rel := range prodAfter {
		if _, ok := refSHAs[rel]; !ok {
			return fmt.Errorf("product mutated extra file %s not matched by rg reference", rel)
		}
	}
	return nil
}

func TestCF08_R03_ReplacementDifferential(t *testing.T) {
	rgVer := cf08CaptureRGVersion(t)
	baseSeed := cf08BaseSeed()
	rep := cf08FamilyReport{
		Family:       "R03",
		Lane:         "in_process",
		BaseSeed:     baseSeed,
		Planned:      cf08R03Instances,
		EdgesPlanned: 6,
		DeclaredGaps: map[string]int{},
		RGVersion:    rgVer,
	}

	encodings := []string{"utf-8", "utf-8-bom", "utf-16-le-bom", "utf-16-be-bom"}
	rootBase := t.TempDir()

	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 12 {
		workers = 12
	}
	jobs := make(chan int, workers*2)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				rng := rand.New(rand.NewSource(cf08DeriveSeed(baseSeed, "R03", i)))
				// Cover the 1..200 file range: mostly 1..8 files for speed, plus periodic larger corpora up to 200.
				fileCount := 1 + rng.Intn(6)
				if i%300 == 0 {
					fileCount = 200
				} else if i%100 == 0 {
					fileCount = 40 + rng.Intn(60)
				}

				tok := cf08Tokens[rng.Intn(len(cf08Tokens))]
				regexMode := rng.Intn(2) == 0
				caseSens := rng.Intn(2) == 0
				wholeWord := !regexMode && rng.Intn(2) == 0

				var pattern, replacement string
				if regexMode {
					pattern = fmt.Sprintf("(%s)_([0-9]{1,2})", tok)
					switch rng.Intn(3) {
					case 0:
						replacement = "rewritten_${2}_$1"
					case 1:
						replacement = "${1}-${2}-$$"
					default:
						replacement = "v$2_${1}"
					}
				} else {
					pattern = tok
					if rng.Intn(2) == 0 {
						replacement = tok + "_$1_${2}_replaced"
					} else {
						replacement = tok + "_replaced"
					}
				}

				files := make(map[string][]byte, fileCount)
				for f := 0; f < fileCount; f++ {
					enc := encodings[rng.Intn(len(encodings))]
					eol := "\n"
					if rng.Intn(3) == 0 {
						eol = "\r\n"
					}
					var sb strings.Builder
					lines := 2 + rng.Intn(4)
					for l := 0; l < lines; l++ {
						lineEOL := eol
						if l%2 == 1 && rng.Intn(4) == 0 {
							if lineEOL == "\n" {
								lineEOL = "\r\n"
							} else {
								lineEOL = "\n"
							}
						}
						t1 := tok
						if l%2 == 1 {
							t1 = strings.ToUpper(tok)
						}
						sb.WriteString(fmt.Sprintf("row_%d: %s_%d pre%s %s.%s", l, t1, l+1, tok, tok, lineEOL))
					}
					rel := fmt.Sprintf("d%d/f_%03d.txt", f%4, f)
					files[rel] = cf08EncodeWithBOM(sb.String(), enc)
				}

				caseDir := filepath.Join(rootBase, "inst_"+strconv.Itoa(i))
				err := cf08RunR03Case(caseDir, files, pattern, replacement, MatchFlags{
					Regex: regexMode, CaseSensitive: caseSens, WholeWord: wholeWord,
				})
				_ = os.RemoveAll(caseDir)

				mu.Lock()
				rep.Executed++
				if err != nil {
					rep.Failed++
					if len(rep.Failures) < 20 {
						rep.Failures = append(rep.Failures, fmt.Sprintf("R03[%d] (pat=%q repl=%q regex=%v cs=%v ww=%v): %v", i, pattern, replacement, regexMode, caseSens, wholeWord, err))
					}
				} else {
					rep.Passed++
				}
				mu.Unlock()
			}
		}()
	}

	for i := 0; i < cf08R03Instances; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// 6 Hand-written edges for R03.
	runEdge := func(name string, files map[string][]byte, pattern, replacement string, flags MatchFlags) {
		rep.EdgesExecuted++
		if err := cf08RunR03Case(t.TempDir(), files, pattern, replacement, flags); err != nil {
			rep.EdgesFailed++
			rep.Failures = append(rep.Failures, fmt.Sprintf("%s: %v", name, err))
		} else {
			rep.EdgesPassed++
		}
	}

	// R03-E01: Literal mode preserves $1, ${2}, $$ verbatim.
	runEdge("R03-E01", map[string][]byte{"lit.txt": []byte("price alpha_1 end\n")}, "alpha_1", "$1_${2}_$$", MatchFlags{Regex: false, CaseSensitive: true})

	// R03-E02: Regex mode expands numbered groups ${2}, $1, and $$ -> $.
	runEdge("R03-E02", map[string][]byte{"re.txt": []byte("item alpha_42 and beta_7\n")}, "([a-z]+)_([0-9]+)", "${2}_$1_$$", MatchFlags{Regex: true, CaseSensitive: true})

	// R03-E03: Whole-word toggle excludes partial word matches.
	runEdge("R03-E03", map[string][]byte{"ww.txt": []byte("alpha alpha. prealpha alpha_1 (alpha)\n")}, "alpha", "OMEGA", MatchFlags{Regex: false, CaseSensitive: true, WholeWord: true})

	// R03-E04: Mixed LF and CRLF lines in one file preserved byte-for-byte.
	runEdge("R03-E04", map[string][]byte{"eol.txt": []byte("one alpha\r\ntwo alpha\nthree alpha\r\n")}, "alpha", "beta", MatchFlags{Regex: false, CaseSensitive: true})

	// R03-E05: All 4 encodings in one corpus with BOMs preserved byte-for-byte.
	runEdge("R03-E05", map[string][]byte{
		"u8.txt":     cf08EncodeWithBOM("alpha_10 line\n", "utf-8"),
		"u8bom.txt":  cf08EncodeWithBOM("alpha_20 line\r\n", "utf-8-bom"),
		"u16le.txt":  cf08EncodeWithBOM("alpha_30 line\r\n", "utf-16-le-bom"),
		"u16be.txt":  cf08EncodeWithBOM("alpha_40 line\n", "utf-16-be-bom"),
	}, "(alpha)_([0-9]+)", "${2}_$1", MatchFlags{Regex: true, CaseSensitive: false})

	// R03-E06: Case-insensitive literal + regex across mixed-case tokens.
	runEdge("R03-E06", map[string][]byte{"case.txt": []byte("PaintedWolf paintedwolf PAINTEDWOLF\n")}, "paintedwolf", "pw", MatchFlags{Regex: false, CaseSensitive: false})

	cf08RecordFamilyReport(t, rep)
	if rep.Failed > 0 || rep.EdgesFailed > 0 {
		t.Fatalf("CF08 R03 failures (%d inst, %d edges): %v", rep.Failed, rep.EdgesFailed, rep.Failures[:min(5, len(rep.Failures))])
	}
}
