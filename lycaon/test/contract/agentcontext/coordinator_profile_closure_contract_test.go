package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// coordinatorPromptlessAllowlist lists coordinator profile tools taught
// outside coordinator prompt prose.
var coordinatorPromptlessAllowlist = map[string]string{
	"survey_repo":         "taught by the survey-first-pass unit",
	"ask_user":            "taught by its schema; prompts say to ask the user",
	"promote_overlay":     "taught by the overlay-promote partial on its surface",
	"reject_overlay":      "taught by the overlay-promote partial on its surface",
	"shelve_overlay":      "taught by the turn-state roster lines for delivered results",
	"worker_history":      "taught by its schema; the roster carries each worker's bounded summary",
	"restore_version":     "worker mutations via task(); coordinator plan surfaces only",
	"source_history":      "universal repo inspection",
	"plan_from_blueprint": "blueprint planning primitive; taught by coordinator-mode-plan-execute and tool schema",
	"task":                "delegation primitive; surfaced via leg-finished kick",
	"workflow_persist":    "low-level upsert; coordinator uses compose/compose_from_template instead",
	"workflow_transition": "taught via active-workflow Phase exit inject (choice arms)",
	"scan_pack":           "host scan to completion; drill-down via scan_list/scan_summary/scan_query documented in pack-board-legend",
	"scan_list":           "scan evidence drill-down; conventions in pack-board-legend partial",
	"scan_summary":        "scan evidence drill-down; conventions in pack-board-legend partial",
	"scan_query":          "scan evidence drill-down; conventions in pack-board-legend partial",
	"scan_compare":        "verify-fix diff after scan_pack; new_scan_id required, old_scan_id optional (PreviousComplete)",
	"render_view":         "visual mockup rasterize on investigate/overlay-promote; calling convention in tool schema",
	"view_image":          "visual inspection on investigate/overlay-promote; calling convention in tool schema",
	"view_video":          "visual inspection on investigate/overlay-promote; calling convention in tool schema",
	"capture_page":        "page capture on investigate/overlay-promote; calling convention in tool schema",
	"measure_page":        "page geometry probe on investigate/overlay-promote; calling convention in tool schema",
	"page_open":           "persistent page open on investigate/overlay-promote; calling convention in tool schema",
	"page_act":            "persistent page act on investigate/overlay-promote; calling convention in tool schema",
	"page_snapshot":       "persistent page snapshot on investigate/overlay-promote; calling convention in tool schema",
	"page_close":          "persistent page close on investigate/overlay-promote; calling convention in tool schema",
	"terminal_open":       "pty open on investigate/overlay-promote; calling convention in tool schema",
	"terminal_send":       "pty send on investigate/overlay-promote; calling convention in tool schema",
	"terminal_read":       "pty read on investigate/overlay-promote; calling convention in tool schema",
	"terminal_snapshot":   "pty vt grid snapshot on investigate/overlay-promote; calling convention in tool schema",
	"terminal_close":      "pty close on investigate/overlay-promote; calling convention in tool schema",
	"command_stop":        "live job termination primitive; taught by the shared runner-operating-procedure unit and tool schema",
	"held_stop":           "held-call cancellation primitive; surfaced by held-call continuation envelopes and tool schema",
	"worker_cancel":       "cancellation primitive; convention in coordinator-worker-chain-kick partial and tool schema",
	"web_search":          "coordinator-core invariant 6; inline external lookup when on surface schema",
	"fetch_url":           "coordinator-core invariant 6; inline external lookup when on surface schema",
	"skills_read":         "skill activation on investigate and overlay-promote; roster and calling convention ship in the shared agent-skills partial, outside the coordinator corpus globs",
	"request_tools":       "schema discovery is taught by the shared deferred-tool-catalog partial in the turn recipe",
	"secret_generate":     "mandatory secret-handling skill and tool schema carry the capability workflow",
	"secret_list":         "mandatory secret-handling skill and tool schema carry the capability workflow",
	"secret_revoke":       "mandatory secret-handling skill and tool schema carry the capability workflow",
	"mcp_*":               "enabled MCP schemas on open-world surfaces; calling convention in each tool schema",
}

func loadCoordinatorProfileTools(t *testing.T) sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, p := range profiles {
		if p.ID == "coordinator" {
			return p
		}
	}
	t.Fatal("coordinator profile missing")
	return sandbox.ToolProfile{}
}

// knownToolUniverse returns catalogued tool names for closure checks.
func knownToolUniverse(t *testing.T, root string) map[string]bool {
	t.Helper()
	universe := map[string]bool{}

	profile := loadCoordinatorProfileTools(t)
	for tool := range profile.Tools {
		universe[tool] = true
	}
	for _, tool := range profile.DenyTools {
		universe[strings.TrimSpace(tool)] = true
	}

	catalog := strings.Split(string(mustRead(t, filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "lycaon-tools.yaml"))), "\n")
	for _, line := range catalog {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		if name := strings.TrimSpace(strings.TrimPrefix(line, "- ")); name != "" {
			universe[name] = true
		}
	}
	return universe
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
