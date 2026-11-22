package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestShippedBannerPairedReachWitnesses(t *testing.T) {
	oarAuditEnv(t)
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)

	cmdWithSed := reachWithProgramsAndSchemas([]string{"command"}, []string{"sed"}, nil)
	cmdWithoutSed := reachWithProgramsAndSchemas([]string{"command"}, []string{"cat"}, nil)
	noCmd := reachWithProgramsAndSchemas([]string{"ask_user"}, nil, nil)

	t.Run("tool_output_truncated_and_spill_capped", func(t *testing.T) {
		spillVars := map[string]any{
			"spill_path":     "tool-output/example.txt",
			"spill_bytes":    4096,
			"original_bytes": 16384,
			"max_file_bytes": 8388608,
			"spill_readable": true,
			"cap":            67108864,
		}
		for _, code := range []string{"TOOL_OUTPUT_TRUNCATED", "TOOL_OUTPUT_SPILL_CAPPED"} {
			withSed := guidance.EnvelopeHintMessage(toolreach.WithRequest(t.Context(), cmdWithSed), cfg, nil, code, spillVars)
			if !strings.Contains(withSed, "`sed -n` in command") {
				t.Fatalf("%s with sed reachable must show sed -n: %q", code, withSed)
			}
			withoutSed := guidance.EnvelopeHintMessage(toolreach.WithRequest(t.Context(), cmdWithoutSed), cfg, nil, code, spillVars)
			if strings.Contains(withoutSed, "sed -n") || !strings.Contains(withoutSed, "offered file-reading capability") {
				t.Fatalf("%s with command offered but sed unreachable must omit sed -n: %q", code, withoutSed)
			}
			absent := guidance.EnvelopeHintMessage(toolreach.WithRequest(t.Context(), noCmd), cfg, nil, code, spillVars)
			if strings.Contains(absent, "sed -n") {
				t.Fatalf("%s without command must omit sed -n: %q", code, absent)
			}
		}
	})

	t.Run("overlay_promote_spill_command_and_preview_overlay_subsets", func(t *testing.T) {
		vars := map[string]any{
			"overlay_id":     "w-job-1",
			"spill_path":     "tool-output/overlay.json",
			"spill_bytes":    8192,
			"spill_readable": true,
		}
		both := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), reachWithProgramsAndSchemas([]string{"command", "preview_overlay"}, []string{"sed"}, nil)),
			cfg, nil, "OVERLAY_PROMOTE_SPILL", vars,
		)
		if !strings.Contains(both, "such as command") || !strings.Contains(both, "preview_overlay(") {
			t.Fatalf("OVERLAY_PROMOTE_SPILL with both tools must render command and preview_overlay: %q", both)
		}
		cmdOnly := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), cmdWithSed),
			cfg, nil, "OVERLAY_PROMOTE_SPILL", vars,
		)
		if !strings.Contains(cmdOnly, "such as command") || strings.Contains(cmdOnly, "preview_overlay(") {
			t.Fatalf("OVERLAY_PROMOTE_SPILL with command only must keep command and omit preview_overlay: %q", cmdOnly)
		}
		neither := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), noCmd),
			cfg, nil, "OVERLAY_PROMOTE_SPILL", vars,
		)
		if strings.Contains(neither, "such as command") || strings.Contains(neither, "preview_overlay(") {
			t.Fatalf("OVERLAY_PROMOTE_SPILL without either tool must omit both: %q", neither)
		}
	})

	t.Run("web_search_duplicate_query_and_workflow_phase_exit_required", func(t *testing.T) {
		withFetch := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), reachWithProgramsAndSchemas([]string{"web_search", "fetch_url"}, nil, nil)),
			cfg, nil, "WEB_SEARCH_DUPLICATE_QUERY", map[string]any{"tool": "web_search"},
		)
		if !strings.Contains(withFetch, "Use fetch_url") {
			t.Fatalf("WEB_SEARCH_DUPLICATE_QUERY with fetch_url must recommend fetch_url: %q", withFetch)
		}
		withoutFetch := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), reachWithProgramsAndSchemas([]string{"web_search"}, nil, nil)),
			cfg, nil, "WEB_SEARCH_DUPLICATE_QUERY", map[string]any{"tool": "web_search"},
		)
		if strings.Contains(withoutFetch, "fetch_url") || !strings.Contains(withoutFetch, "Refine the query") {
			t.Fatalf("WEB_SEARCH_DUPLICATE_QUERY without fetch_url must omit fetch_url: %q", withoutFetch)
		}

		wfVars := map[string]any{"workflow_id": "wf-1", "phase": "intake"}
		withAdvance := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), reachWithProgramsAndSchemas([]string{"workflow_advance"}, nil, nil)),
			cfg, nil, "WORKFLOW_PHASE_EXIT_REQUIRED", wfVars,
		)
		if !strings.Contains(withAdvance, "Call `workflow_advance` now.") {
			t.Fatalf("WORKFLOW_PHASE_EXIT_REQUIRED with workflow_advance must recommend call: %q", withAdvance)
		}
		withoutAdvance := guidance.EnvelopeHintMessage(
			toolreach.WithRequest(t.Context(), noCmd),
			cfg, nil, "WORKFLOW_PHASE_EXIT_REQUIRED", wfVars,
		)
		if strings.Contains(withoutAdvance, "Call `workflow_advance`") || !strings.Contains(withoutAdvance, "`workflow_advance` is not offered on this request") {
			t.Fatalf("WORKFLOW_PHASE_EXIT_REQUIRED without workflow_advance must report unavailability: %q", withoutAdvance)
		}
	})

	t.Run("worker_url_not_observed_fetch_url_reach", func(t *testing.T) {
		_, pipeline, _, _ := effectiveOARBlockPlane(t)
		evalWorker := func(sid string, reach toolreach.Request) string {
			ctx := toolreach.WithRequest(t.Context(), reach)
			gc := oar.NewGuardContext()
			gc.Session.SessionID, gc.Session.Role, gc.Session.WorkerLeg = sid, "worker", true
			gc.Grounding.WorkerSummaryPresent = true
			gc.Grounding.UnobservedCitedURLs = []string{"https://example.com/unseen"}
			gc.ObservationData = map[string]any{"offender_count": 1, "offenders_sample": "https://example.com/unseen"}
			tools.RegisterRecoveryFacts(ctx, gc)
			res, rErr := pipeline.EvaluateBlock(ctx, oar.AnchorWorkerReportCheck, gc)
			contractcheck.FailErr(t, "EvaluateBlock WORKER_URL_NOT_OBSERVED", rErr)
			return res.Decision.Copy["fix"]
		}
		if got := evalWorker("w-fetch-1", reachWithProgramsAndSchemas([]string{"fetch_url"}, nil, nil)); !strings.Contains(got, "fetch_url") {
			t.Fatalf("expected fetch_url recovery when callable: %q", got)
		}
		if got := evalWorker("w-fetch-2", noCmd); strings.Contains(got, "fetch_url") || !strings.Contains(got, "Drop the unobserved URLs") {
			t.Fatalf("expected drop-citation exit without fetch_url: %q", got)
		}
	})
}
