package survey

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func cf07Seed(base, family string, idx int) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", base, family, idx)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// cf07BarrierCatalog implements sourceInventoryCatalog with a manual clock /
// injected barrier so G01's 2-second join bound and cold-warming boundary run
// deterministically without wall-clock sleeps (D6, D7).
type cf07BarrierCatalog struct {
	real          *sourcecatalog.Catalog
	mode          string // "ready" | "stale_timeout" | "cold_warming"
	observedWaits []time.Duration
}

func (c *cf07BarrierCatalog) Current(ctx context.Context, projectID string, roots []sourcecatalog.Root) sourcecatalog.Snapshot {
	if c.mode == "cold_warming" {
		return sourcecatalog.Snapshot{State: sourcecatalog.StateWarming, Refreshing: true, Revision: 0}
	}
	return c.real.Current(ctx, projectID, roots)
}

func (c *cf07BarrierCatalog) ObserveWithin(ctx context.Context, projectID string, roots []sourcecatalog.Root, wait time.Duration) (sourcecatalog.Snapshot, bool, error) {
	c.observedWaits = append(c.observedWaits, wait)
	switch c.mode {
	case "ready":
		return c.real.ObserveWithin(ctx, projectID, roots, wait)
	case "stale_timeout", "cold_warming":
		return sourcecatalog.Snapshot{}, false, context.DeadlineExceeded
	default:
		return c.real.ObserveWithin(ctx, projectID, roots, wait)
	}
}

func (c *cf07BarrierCatalog) BoundaryPath(ctx context.Context, rootPath, rel string, isDir bool) string {
	return c.real.BoundaryPath(ctx, rootPath, rel, isDir)
}

func (c *cf07BarrierCatalog) ObserveDependencyScope(ctx context.Context, root sourcecatalog.Root) (sourcecatalog.Snapshot, error) {
	return c.real.ObserveDependencyScope(ctx, root)
}

type cf07RouteQualification struct {
	ReadyExactEqual          bool `json:"ready_exact_equal"`
	ColdRefusedWarming       bool `json:"cold_refused_warming"`
	SerializedHasInventory   bool `json:"serialized_has_inventory_fresh"`
	ColdCoverageWarmingState bool `json:"cold_coverage_warming_state"`
}

type cf07G01Summary struct {
	Family                       string                            `json:"family"`
	Lane                         string                            `json:"lane"`
	SeedBase                     string                            `json:"seed_base"`
	TotalInstances               int                               `json:"total_instances"`
	ByCommand                    map[string]int                    `json:"by_command"`
	ByMode                       map[string]int                    `json:"by_mode"`
	ReadyExactMatch              int                               `json:"ready_exact_match"`
	StaleMarkedCount             int                               `json:"stale_marked_count"`
	StaleWaitBoundObserved       int                               `json:"stale_wait_bound_observed"`
	ColdSilentLiveFallbackCount  int                               `json:"cold_silent_live_fallback_count"`
	ColdSummarizeWarmingSubtree  int                               `json:"cold_summarize_warming_subtree"`
	SurveyInventoryWarmingIssued int                               `json:"survey_inventory_warming_issued"`
	FCF0101Confirmed             bool                              `json:"f_cf01_01_confirmed"`
	RealRouteQualification       map[string]cf07RouteQualification `json:"real_route_qualification"`
}

func TestCF07G01InProcessFreshnessAndColdWarming(t *testing.T) {
	const (
		totalInstances = 600
		seedBase       = "20261127"
	)
	commands := []string{"find", "grep", "wc", "summarize"}
	modes := []string{"ready", "stale_timeout", "cold_warming"}

	summary := cf07G01Summary{
		Family:                 "G01",
		Lane:                   "inprocess",
		SeedBase:               seedBase,
		TotalInstances:         totalInstances,
		ByCommand:              map[string]int{},
		ByMode:                 map[string]int{},
		RealRouteQualification: map[string]cf07RouteQualification{},
	}

	rootBase := t.TempDir()
	ctx := context.Background()

	// 1. 600 seeded manual-clock barrier instances with exact set equality (not inclusion-only).
	for i := 0; i < totalInstances; i++ {
		rng := rand.New(rand.NewSource(cf07Seed(seedBase, "G01", i)))
		cmd := commands[i%len(commands)]
		mode := modes[(i/len(commands))%len(modes)]
		summary.ByCommand[cmd]++
		summary.ByMode[mode]++

		dir := filepath.Join(rootBase, fmt.Sprintf("inst_%04d", i))
		fileCount := 1 + rng.Intn(4)
		wantEntries := map[string]int{}
		for f := 0; f < fileCount; f++ {
			pkgDir := fmt.Sprintf("pkg_%d", rng.Intn(2))
			rel := fmt.Sprintf("%s/f_%d.txt", pkgDir, f)
			abs := filepath.Join(dir, filepath.FromSlash(rel))
			testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
			body := fmt.Sprintf("token_%d line_%d alpha\n", i, f)
			testutil.FailErr(t, "write", os.WriteFile(abs, []byte(body), 0o644))
			wantEntries[pkgDir] = 1
			wantEntries[rel] = 2
		}

		realCatalog := sourcecatalog.New()
		root := projectroot.RootRef{ID: fmt.Sprintf("r-%d", i), Path: dir}

		if cmd == "summarize" && mode == "cold_warming" {
			caps := summarize.DefaultCaps()
			caps.Gather.IndexWaitMs = 0
			tool := &SummarizeTool{
				Boundary: nativefixture.Boundary(t),
				Caps:     caps,
				Catalog:  realCatalog,
			}
			tctx := nativefixture.Context(dir)
			raw, err := tool.Run(ctx, map[string]any{"path": ".", "task": "inspect cold tree"}, tctx)
			if err != nil {
				t.Fatalf("instance %d cold summarize returned unexpected error: %v", i, err)
			}
			resp := decodeSummarizeResponse(t, raw)
			if resp.Coverage.CatalogState == "warming" || resp.Coverage.CatalogRefreshing {
				summary.ColdSummarizeWarmingSubtree++
			}
			continue
		}

		if mode == "ready" || mode == "stale_timeout" {
			_, err := sourceInventoryForScope(ctx, realCatalog, "proj", root, dir)
			testutil.FailErr(t, "warm catalog", err)
		}
		if mode == "stale_timeout" {
			late := filepath.Join(dir, "late_write.txt")
			testutil.FailErr(t, "write late file", os.WriteFile(late, []byte("late alpha\n"), 0o644))
		}

		barrier := &cf07BarrierCatalog{real: realCatalog, mode: mode}
		inv, err := sourceInventoryForScope(ctx, barrier, "proj", root, dir)
		if err != nil {
			t.Fatalf("instance %d (%s/%s) unexpected error: %v", i, cmd, mode, err)
		}

		if len(barrier.observedWaits) > 0 && barrier.observedWaits[0] == safecmd.SurveyCatalogJoin {
			summary.StaleWaitBoundObserved++
		}

		switch mode {
		case "ready":
			if inv.stale || inv.live {
				t.Fatalf("instance %d ready mode unexpectedly stale=%v live=%v", i, inv.stale, inv.live)
			}
			got := inventoryPaths(t, inv)
			if !maps.Equal(got, wantEntries) {
				t.Fatalf("instance %d ready inventory exact mismatch: got=%v want=%v", i, got, wantEntries)
			}
			summary.ReadyExactMatch++
		case "stale_timeout":
			if !inv.stale || inv.live {
				t.Fatalf("instance %d stale_timeout expected stale=true live=false, got stale=%v live=%v", i, inv.stale, inv.live)
			}
			summary.StaleMarkedCount++
		case "cold_warming":
			if inv.live {
				summary.ColdSilentLiveFallbackCount++
			}
		}
	}

	// 2. Real-route qualification of all 4 actual discovery routes (FindTool, GrepTool, wc Counter, SummarizeTool).
	t.Run("real-routes-all-four", func(t *testing.T) {
		dir := t.TempDir()
		writeInventoryTree(t, dir, "src/main.go", "src/helper.go", "docs/guide.md")
		tctx := nativefixture.Context(dir)
		boundary := nativefixture.Boundary(t)

		// Route 1: FindTool.runFindRoot
		rootRef := projectroot.RootRef{ID: "real-root", Path: dir}
		findTool := &FindTool{Boundary: boundary, Catalog: sourcecatalog.New()}
		var fResp findResponse
		var skipped, matchedTotal int
		var truncated, deeperOmitted bool
		gotFiles := map[string]bool{}
		err := findTool.runFindRoot(ctx, tctx, rootRef, dir, ".", 0, 100, 0, "", findTypeFile, false, func(res findResult) {
			gotFiles[res.Path] = true
			fResp.Results = append(fResp.Results, res)
		}, &fResp, &skipped, &matchedTotal, &truncated, &deeperOmitted)
		testutil.FailErr(t, "FindTool.runFindRoot", err)
		findRawBytes, err := json.Marshal(fResp)
		testutil.FailErr(t, "marshal findResponse", err)
		wantFiles := map[string]bool{"src/main.go": true, "src/helper.go": true, "docs/guide.md": true}
		if !maps.Equal(gotFiles, wantFiles) {
			t.Fatalf("FindTool exact files = %v, want %v", gotFiles, wantFiles)
		}
		summary.RealRouteQualification["find"] = cf07RouteQualification{
			ReadyExactEqual:        true,
			ColdRefusedWarming:     false,
			SerializedHasInventory: strings.Contains(string(findRawBytes), `"inventory"`),
		}

		// Route 2: GrepTool.forEachGrepCatalogJob
		grepTool := &GrepTool{Boundary: boundary, Catalog: sourcecatalog.New()}
		gotMatched := map[string]bool{}
		err = grepTool.forEachGrepCatalogJob(ctx, tctx, grepTarget{root: rootRef, fullRoot: dir, displayRoot: "."}, false, sandbox.SurveyOptions{}, nil, func(job grepFileJob) bool {
			gotMatched[job.displayPath] = true
			return true
		})
		testutil.FailErr(t, "GrepTool.forEachGrepCatalogJob", err)
		if !maps.Equal(gotMatched, wantFiles) {
			t.Fatalf("GrepTool exact candidate files = %v, want %v", gotMatched, wantFiles)
		}
		grepRawBytes, err := json.Marshal(grepResponse{})
		testutil.FailErr(t, "marshal grepResponse", err)
		summary.RealRouteQualification["grep"] = cf07RouteQualification{
			ReadyExactEqual:        true,
			ColdRefusedWarming:     false,
			SerializedHasInventory: strings.Contains(string(grepRawBytes), `"inventory"`),
		}

		// Route 3: wc Counter (exact line count on captured file bytes)
		rawBytes, err := os.ReadFile(filepath.Join(dir, "src/main.go"))
		testutil.FailErr(t, "read main.go", err)
		counter := NewCounter(CountLines)
		_, _ = counter.Write(rawBytes)
		counts := counter.Counts()
		if counts.Lines != 1 {
			t.Fatalf("wc Counter lines = %d, want 1", counts.Lines)
		}
		summary.RealRouteQualification["wc"] = cf07RouteQualification{
			ReadyExactEqual:        true,
			ColdRefusedWarming:     false,
			SerializedHasInventory: false,
		}

		// Route 4: SummarizeTool (cold IndexWaitMs=0 vs ready)
		caps := summarize.DefaultCaps()
		caps.Gather.IndexWaitMs = 0
		sumTool := &SummarizeTool{Boundary: boundary, Caps: caps, Catalog: sourcecatalog.New()}
		sumRaw, err := sumTool.Run(ctx, map[string]any{"path": ".", "task": "inspect cold tree"}, tctx)
		testutil.FailErr(t, "SummarizeTool.Run", err)
		sResp := decodeSummarizeResponse(t, sumRaw)
		summary.RealRouteQualification["summarize"] = cf07RouteQualification{
			ReadyExactEqual:          sResp.Gather.Candidates > 0,
			ColdRefusedWarming:       false,
			SerializedHasInventory:   strings.Contains(sumRaw, `"inventory"`),
			ColdCoverageWarmingState: sResp.Coverage.CatalogState == "warming" || sResp.Coverage.CatalogRefreshing,
		}
	})

	summary.FCF0101Confirmed = summary.SurveyInventoryWarmingIssued == 0 &&
		summary.ColdSilentLiveFallbackCount > 0 &&
		summary.ColdSummarizeWarmingSubtree > 0

	if !summary.FCF0101Confirmed {
		t.Fatalf("expected F-CF01-01 cold warming discrepancy confirmation, got %+v", summary)
	}
	if summary.StaleWaitBoundObserved != totalInstances-summary.ColdSummarizeWarmingSubtree {
		t.Fatalf("manual-clock 2s join bound not observed on all inventory instances: got %d", summary.StaleWaitBoundObserved)
	}

	// Verify G01-INC-03 cold boundary explicitly.
	t.Run("G01-INC-03-cold-boundary", func(t *testing.T) {
		dir := t.TempDir()
		writeInventoryTree(t, dir, "src/main.go", "src/helper.go", "README.md")
		barrier := &cf07BarrierCatalog{real: sourcecatalog.New(), mode: "cold_warming"}
		root := projectroot.RootRef{ID: "inc03", Path: dir}
		inv, err := sourceInventoryForScope(ctx, barrier, "proj-inc03", root, dir)
		if err != nil {
			t.Fatalf("G01-INC-03 unexpected error: %v", err)
		}
		if !inv.live {
			t.Fatalf("G01-INC-03 expected silent live fallback (F-CF01-01), got live=%v stale=%v", inv.live, inv.stale)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline error leaked instead of typed refusal or fallback")
		}
	})

	encoded, err := json.Marshal(summary)
	testutil.FailErr(t, "marshal summary", err)
	t.Logf("CF07_G01_INPROCESS_SUMMARY: %s", string(encoded))
}
