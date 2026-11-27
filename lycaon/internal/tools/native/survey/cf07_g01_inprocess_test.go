package survey

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
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

type cf07G01Summary struct {
	Family                       string         `json:"family"`
	Lane                         string         `json:"lane"`
	SeedBase                     string         `json:"seed_base"`
	TotalInstances               int            `json:"total_instances"`
	ByCommand                    map[string]int `json:"by_command"`
	ByMode                       map[string]int `json:"by_mode"`
	ReadyExactMatch              int            `json:"ready_exact_match"`
	StaleMarkedCount             int            `json:"stale_marked_count"`
	StaleWaitBoundObserved       int            `json:"stale_wait_bound_observed"`
	ColdSilentLiveFallbackCount  int            `json:"cold_silent_live_fallback_count"`
	ColdSummarizeWarmingSubtree  int            `json:"cold_summarize_warming_subtree"`
	SurveyInventoryWarmingIssued int            `json:"survey_inventory_warming_issued"`
	FCF0101Confirmed             bool           `json:"f_cf01_01_confirmed"`
}

func TestCF07G01InProcessFreshnessAndColdWarming(t *testing.T) {
	const (
		totalInstances = 600
		seedBase       = "20261127"
	)
	commands := []string{"find", "grep", "wc", "summarize"}
	modes := []string{"ready", "stale_timeout", "cold_warming"}

	summary := cf07G01Summary{
		Family:         "G01",
		Lane:           "inprocess",
		SeedBase:       seedBase,
		TotalInstances: totalInstances,
		ByCommand:      map[string]int{},
		ByMode:         map[string]int{},
	}

	rootBase := t.TempDir()
	ctx := context.Background()

	for i := 0; i < totalInstances; i++ {
		rng := rand.New(rand.NewSource(cf07Seed(seedBase, "G01", i)))
		cmd := commands[i%len(commands)]
		mode := modes[(i/len(commands))%len(modes)]
		summary.ByCommand[cmd]++
		summary.ByMode[mode]++

		dir := filepath.Join(rootBase, fmt.Sprintf("inst_%04d", i))
		fileCount := 1 + rng.Intn(4)
		wantFiles := map[string]bool{}
		for f := 0; f < fileCount; f++ {
			rel := fmt.Sprintf("pkg_%d/f_%d.txt", rng.Intn(2), f)
			abs := filepath.Join(dir, filepath.FromSlash(rel))
			testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
			body := fmt.Sprintf("token_%d line_%d alpha\n", i, f)
			testutil.FailErr(t, "write", os.WriteFile(abs, []byte(body), 0o644))
			wantFiles[rel] = true
		}

		realCatalog := sourcecatalog.New()
		root := projectroot.RootRef{ID: fmt.Sprintf("r-%d", i), Path: dir}

		if cmd == "summarize" && mode == "cold_warming" {
			// Exercise SummarizeTool's cold warming path with zero wait (manual-clock
			// equivalent of cold catalog with no complete generation).
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

		// Warm realCatalog for ready and stale_timeout modes.
		if mode == "ready" || mode == "stale_timeout" {
			_, err := sourceInventoryForScope(ctx, realCatalog, "proj", root, dir)
			testutil.FailErr(t, "warm catalog", err)
		}
		if mode == "stale_timeout" {
			// Mutate tree after complete generation without refreshing realCatalog.
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
			matched := true
			for rel := range wantFiles {
				if _, ok := got[rel]; !ok {
					matched = false
					break
				}
			}
			if !matched {
				t.Fatalf("instance %d ready inventory missing files: got=%v want=%v", i, got, wantFiles)
			}
			summary.ReadyExactMatch++
		case "stale_timeout":
			if !inv.stale || inv.live {
				t.Fatalf("instance %d stale_timeout expected stale=true live=false, got stale=%v live=%v", i, inv.stale, inv.live)
			}
			summary.StaleMarkedCount++
		case "cold_warming":
			// F-CF01-01 verification: docs specify SURVEY_INVENTORY_WARMING refusal,
			// but D1 runtime silently sets inv.live=true instead of returning an error.
			if inv.live {
				summary.ColdSilentLiveFallbackCount++
			}
		}
	}

	summary.FCF0101Confirmed = summary.SurveyInventoryWarmingIssued == 0 &&
		summary.ColdSilentLiveFallbackCount > 0 &&
		summary.ColdSummarizeWarmingSubtree > 0

	if !summary.FCF0101Confirmed {
		t.Fatalf("expected F-CF01-01 cold warming discrepancy confirmation, got %+v", summary)
	}
	if summary.StaleWaitBoundObserved != totalInstances-summary.ColdSummarizeWarmingSubtree {
		t.Fatalf("manual-clock 2s join bound not observed on all inventory instances: got %d", summary.StaleWaitBoundObserved)
	}

	// Verify G01-INC-03 cold boundary with SummarizeTool explicitly.
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
