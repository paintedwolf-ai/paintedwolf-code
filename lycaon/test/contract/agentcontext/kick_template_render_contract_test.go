package contract

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestKickTemplatesRenderWithKickEngineVars validates required rendered phrases.
func TestKickTemplatesRenderWithKickEngineVars(t *testing.T) {
	t.Parallel()
	catalogfixture.AssertNoKickConstFiles(t)
	reg := catalogfixture.LoadInformBindings(t)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	// Use every variable supplied by KickEngine.
	data := map[string]any{
		"batch_phase":          "integrate",
		"worker_digest":        "ENV report: 2 files changed, build green.",
		"topology_output":      "Recon pack: 3 risks surfaced.",
		"evidence_digest":      "## Evidence digest (curated)\n[grep#1] main.go:1 — package main\nselected/total: 1/1",
		"options_criterion":    "Prefer minimal long-term surface area.",
		"pending_overlay_jobs": []string{"job-a", "job-b"},
		"partial_worker_jobs":  []string{"job-partial"},
		"promoted_paths":       []string{"lycaon/internal/x.go", "lycaon/internal/y.go"},
		"completed_ago":        "2m ago",
		"scan_id":              "scan-1234",
		"scanner_id":           "fixture-scanner",
		"introduced_count":     1,
		"fixed_count":          1,
		"introduced":           []map[string]any{{"level": "high", "rule_id": "fixture-rule", "path": "main.go", "line": 4, "message": "fixture finding"}},
		"fixed":                []map[string]any{{"level": "high", "rule_id": "fixture-rule", "path": "fixed.go", "line": 8}},
		"batches":              6,
		"tools":                []string{"git_diff", "read"},
		"scan_status":          "complete",
		"scan_categories":      "sast, secret",
		"scan_findings_count":  7,
		"failed_leaves":        []string{"hitl_consulted:intake"},
		"gate_obligations": []map[string]any{{
			"id":      "hitl_consulted:intake",
			"purpose": "Plan intake is freeform must-consult.",
			"satisfy": []string{"Call ask_user with task-appropriate questions when no host card is pending."},
		}},
	}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() || !strings.HasPrefix(b.Render, "coordinator-") {
			continue
		}
		id := catalogfixture.BindingShortID(b.Render)
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
			defer cancel()
			out, err := engine.RenderKick(ctx, b.Render, data)
			if err != nil {
				t.Fatalf("RenderKick %s: %v", b.Render, err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatalf("%s rendered empty under maximal data set", b.Render)
			}
		})
	}
}

// Kicks remain informative when event metadata is absent.
func TestKickTemplatesRenderWithMinimalDataAreNonEmpty(t *testing.T) {
	t.Parallel()
	ids, err := kickTemplateIDs()
	contractcheck.FailErr(t, "kickTemplateIDs", err)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	data := map[string]any{}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	var empty []string
	for _, id := range ids {
		if !strings.HasPrefix(id, "coordinator-") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
		out, err := engine.RenderKick(ctx, id, data)
		cancel()
		if err != nil {
			t.Fatalf("RenderKick %s: %v", id, err)
		}
		if strings.TrimSpace(out) == "" {
			empty = append(empty, id)
		}
	}
	sort.Strings(empty)
	if len(empty) > 0 {
		t.Fatalf("kick templates that render empty under minimal data — every kick must produce baseline prose: %v", empty)
	}
}
