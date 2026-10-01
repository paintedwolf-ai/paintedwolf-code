package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// coordinatorPromptCorpusGlobs matches coordinator-facing prompt templates.
var coordinatorPromptCorpusGlobs = []string{
	"lycaon/config/packs/painted-wolf/platform/agents/prompts/coordinator*.md",
	"lycaon/config/packs/painted-wolf/platform/guidance/coordinator-*.md",
	"lycaon/config/packs/painted-wolf/implement/agents/prompts/coordinator*.md",
}

// coordinatorPromptlessAllowlist lists coordinator profile tools documented
// outside tripartite coordinator prose.
var coordinatorPromptlessAllowlist = map[string]string{
	"grep":                "documented in coordinator-surface-build.md via lane_s_sync_tools template var",
	"survey_repo":         "survey-first-pass partial; catalog bundle composite survey",
	"find":                "documented in coordinator-surface-build.md via lane_s_sync_tools template var; scouts use via task()",
	"list_dir":            "documented in coordinator-surface-build.md via lane_s_sync_tools template var",
	"read":                "documented in coordinator-surface-build.md via lane_s_sync_tools template var",
	"write":               "documented in coordinator-surface-build.md; wrapup partial lists as forbidden",
	"edit":                "documented in coordinator-surface-build.md; wrapup partial lists as forbidden",
	"replace_lines":       "worker long-file mutations via task(); coordinator plan surfaces only",
	"restore_version":     "worker mutations via task(); coordinator plan surfaces only",
	"git_status":          "universal repo inspection",
	"git_diff":            "universal repo inspection",
	"git_log":             "universal repo inspection",
	"git_show":            "universal repo inspection",
	"git_blame":           "universal repo inspection",
	"source_history":      "universal repo inspection",
	"git_ref":             "universal repo inspection",
	"git_branches":        "universal repo inspection",
	"git_commit":          "implement_investigate surface; orchestrate modes delegate via task()",
	"git_checkout":        "branch checkout convention supplied by its tool schema",
	"git_compare":         "ref comparison convention supplied by its tool schema",
	"git_merge":           "merge convention supplied by its tool schema",
	"git_stash":           "stash mutation convention supplied by its tool schema",
	"git_stash_list":      "stash inspection convention supplied by its tool schema",
	"git_restore":         "implement_investigate surface; orchestrate modes delegate via task()",
	"chmod":               "implement_investigate surface; orchestrate modes delegate via task()",
	"delete":              "implement_investigate surface; orchestrate modes delegate via task()",
	"copy":                "implement_investigate surface; orchestrate modes delegate via task()",
	"move":                "implement_investigate surface; orchestrate modes delegate via task()",
	"mkdir":               "implement_investigate surface; orchestrate modes delegate via task()",
	"chown":               "implement_investigate surface; orchestrate modes delegate via task()",
	"extract_archive":     "implement_investigate surface; orchestrate modes delegate via task()",
	"stat":                "implement_investigate surface; orchestrate modes delegate via task()",
	"wc":                  "implement_investigate surface; orchestrate modes delegate via task()",
	"code_rewrite":        "implement_investigate surface; orchestrate modes delegate via task()",
	"jq_edit":             "structured document write on implement_investigate; calling convention in tool schema",
	"process_list":        "implement_investigate surface; orchestrate modes delegate via task()",
	"process_signal":      "implement_investigate surface; orchestrate modes delegate via task()",
	"delegate_dispatch":   "delegation primitive; calling convention covered by topology docs",
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
	"diff":                "two-path file compare on investigate surface; calling convention in tool schema",
	"jq":                  "structured JSON/YAML/TOML query on investigate surface; calling convention in tool schema",
	"worker_cancel":       "cancellation primitive; convention in coordinator-worker-chain-kick partial and tool schema",
	"web_search":          "coordinator-core invariant 6; inline external lookup when on surface schema",
	"fetch_url":           "coordinator-core invariant 6; inline external lookup when on surface schema",
	"skills_read":         "skill activation on investigate and overlay-promote; roster and calling convention ship in the shared agent-skills partial, outside the coordinator corpus globs",
	"surface_note":        "visible-update discipline is injected from the surface-note-discipline shared partial",
	"request_tools":       "schema discovery is taught by the shared deferred-tool-catalog partial in the turn recipe",
	"http_request":        "native outbound request; schema carries the full calling convention",
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

func TestCoordinatorProfileTripartiteClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	profile := loadCoordinatorProfileTools(t)

	promptText := loadCoordinatorPromptCorpus(t, root)

	var missing []string
	for tool, enabled := range profile.Tools {
		if !enabled {
			continue
		}
		if _, ok := coordinatorPromptlessAllowlist[tool]; ok {
			continue
		}
		if !mentionsToolName(promptText, tool) {
			missing = append(missing, tool)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("coordinator profile grants tools that no coordinator prompt mentions: %v\n"+
			"Either reference the tool in one of the files matched by:\n  %s\nor add an entry to coordinatorPromptlessAllowlist with a rationale.",
			missing, strings.Join(coordinatorPromptCorpusGlobs, "\n  "))
	}

	// Allowlist entries map to real profile tools.
	for tool := range coordinatorPromptlessAllowlist {
		if !profile.Tools[tool] {
			t.Fatalf("coordinatorPromptlessAllowlist references %q which is not enabled on the coordinator profile", tool)
		}
	}
}

func TestCoordinatorPromptsCiteOnlyEnabledTools(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	profile := loadCoordinatorProfileTools(t)

	// Pull from the T1 registry catalog to include any tool the coordinator can call.
	known := knownToolUniverse(t, root)

	denied := make(map[string]bool, len(profile.DenyTools))
	for _, name := range profile.DenyTools {
		denied[strings.TrimSpace(name)] = true
	}

	promptText := loadCoordinatorPromptCorpus(t, root)

	var orphans []string
	for tool := range known {
		if !mentionsToolName(promptText, tool) {
			continue
		}
		// Flag tools cited in the playbook that are neither enabled nor denied.
		if denied[tool] || profile.Tools[tool] {
			continue
		}
		orphans = append(orphans, tool+" (cited in playbook but not enabled or denied on profile)")
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Fatalf("coordinator prompts cite tools the profile does not grant:\n  %s", strings.Join(orphans, "\n  "))
	}
}

func loadCoordinatorPromptCorpus(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	files := discoverCoordinatorPromptFiles(t, root)
	if len(files) == 0 {
		t.Fatalf("coordinator prompt corpus is empty — globs %v matched nothing under %s", coordinatorPromptCorpusGlobs, root)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

// discoverCoordinatorPromptFiles expands coordinatorPromptCorpusGlobs on disk.
func discoverCoordinatorPromptFiles(t *testing.T, root string) []string {
	t.Helper()
	var all []string
	seen := map[string]bool{}
	for _, glob := range coordinatorPromptCorpusGlobs {
		matches, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			t.Fatalf("glob %q: %v", glob, err)
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			all = append(all, m)
		}
	}
	sort.Strings(all)
	return all
}

// mentionsToolName checks for the tool name at word boundaries to avoid partial matches.
func mentionsToolName(corpus, tool string) bool {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return false
	}
	return containsWord(corpus, tool)
}

func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] != needle {
			continue
		}
		if !isWordBoundary(haystack, i-1) {
			continue
		}
		if !isWordBoundary(haystack, i+len(needle)) {
			continue
		}
		return true
	}
	return false
}

func isWordBoundary(s string, idx int) bool {
	if idx < 0 || idx >= len(s) {
		return true
	}
	c := s[idx]
	switch {
	case c >= 'a' && c <= 'z':
		return false
	case c >= 'A' && c <= 'Z':
		return false
	case c >= '0' && c <= '9':
		return false
	case c == '_':
		return false
	}
	return true
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
