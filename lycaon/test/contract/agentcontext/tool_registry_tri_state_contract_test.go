package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
	"gopkg.in/yaml.v3"
)

// toolsSchemaExempt lists registered tools whose JSON schema is not in tools/schemas.
var toolsSchemaExempt = map[string]string{
	"command":      "host command handler is constructed by the runtime",
	"scan_list":    "native scan drill-down list",
	"scan_summary": "native scan drill-down summary",
	"scan_query":   "native scan drill-down query",
	"scan_compare": "native scan compare — schema registered at serve boot via RegisterScanTools",
}

// toolsNativeSchemaOnly lists tools/schemas entries not listed in native-tools.yaml.
var toolsNativeSchemaOnly = map[string]string{
	"command":                        "host shell — registered at serve boot, not native manifest",
	"command_output":                 "background command companion — registered at serve boot",
	"command_stop":                   "background command companion — registered at serve boot",
	"held_result":                    "held call companion — registered at serve boot",
	"held_stop":                      "held call companion — registered at serve boot",
	"fetch_url":                      "web research — registered at serve boot",
	"request_decision":               "worker decision escalation — registered at serve boot",
	"request_budget":                 "worker budget request — registered at serve boot",
	"extend_worker_budget":           "coordinator worker budget tool — registered at serve boot",
	"decline_worker_budget":          "coordinator worker budget tool — registered at serve boot",
	"record_finding":                 "findings feed writer — registered at serve boot",
	"scan_pack":                      "scan orchestration — schema built from the live scanner registry at serve boot",
	"scan_list":                      "native scan drill-down — schema registered at serve boot via RegisterScanTools",
	"scan_summary":                   "native scan drill-down — schema registered at serve boot via RegisterScanTools",
	"scan_query":                     "native scan drill-down — schema registered at serve boot via RegisterScanTools",
	"scan_compare":                   "native scan compare — schema registered at serve boot via RegisterScanTools",
	"update_progress":                "coordinator progress writer — registered at serve boot",
	"web_search":                     "web research — registered at serve boot",
	"promote_overlay":                "coordinator overlay surface — registered at serve boot",
	"reject_overlay":                 "coordinator overlay surface — registered at serve boot",
	"preview_overlay":                "coordinator overlay surface — registered at serve boot",
	"wait":                           "coordinator wait tool — registered at serve boot",
	"task":                           "worker spawn tool — registered at serve boot",
	"worker_cancel":                  "coordinator worker tool — registered at serve boot",
	"answer_decision":                "coordinator worker decision tool — registered at serve boot",
	"delegate_dispatch":              "delegation tool — registered at serve boot",
	"delegate_init":                  "delegation tool — registered at serve boot",
	"delegate_status":                "delegation tool — registered at serve boot",
	"delegate_decompose":             "delegation tool — registered at serve boot",
	"pack_board":                     "board tool — registered at serve boot",
	"parse_decomposition":            "parser tool — registered at serve boot",
	"parse_delegation_plan":          "parser tool — registered at serve boot",
	"parse_evaluation":               "parser tool — registered at serve boot",
	"parse_extract_json":             "parser tool — registered at serve boot",
	"parse_validate":                 "parser tool — registered at serve boot",
	"plan_append_review_evidence":    "plan review tool — registered at serve boot",
	"workflow_advance":               "workflow tool — registered at serve boot",
	"workflow_transition":            "workflow tool — registered at serve boot",
	"fanout_plan":                    "workflow tool — registered at serve boot",
	"submit_verdict":                 "workflow tool — registered at serve boot",
	"workflow_catalog_summaries":     "workflow tool — registered at serve boot",
	"workflow_compose":               "workflow tool — registered at serve boot",
	"workflow_compose_from_template": "workflow tool — registered at serve boot",
	"workflow_persist":               "workflow tool — registered at serve boot",
	"workflow_user_feedback":         "workflow tool — registered at serve boot",
	"ask_user":                       "coordinator ask_user — registered at serve boot",
	"state_start":                    "workflow state tool — registered at serve boot",
	"state_close":                    "workflow state tool — registered at serve boot",
	"state_query":                    "workflow state tool — registered at serve boot",
	"state_update":                   "workflow state tool — registered at serve boot",
	"handoff_health":                 "sibling worker coordination — registered at serve boot",
	"handoff_init":                   "sibling worker coordination — registered at serve boot",
	"handoff_release":                "sibling worker coordination — registered at serve boot",
	"handoff_release_all":            "sibling worker coordination — registered at serve boot",
	"handoff_reserve":                "sibling worker coordination — registered at serve boot",
}

// toolsProfileSchemaExempt lists profile tools whose schema is built dynamically in
// Go at serve boot (runtime registry state), so they have no static tools/schemas row.
var toolsProfileSchemaExempt = map[string]string{
	"scan_pack":    "scan orchestration — schema built from the live scanner registry at serve boot",
	"scan_list":    "native scan drill-down — schema registered at serve boot via RegisterScanTools",
	"scan_summary": "native scan drill-down — schema registered at serve boot via RegisterScanTools",
	"scan_query":   "native scan drill-down — schema registered at serve boot via RegisterScanTools",
	"scan_compare": "native scan compare — schema registered at serve boot via RegisterScanTools",
}

func loadToolSchemaNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load tools/schemas", err)
	out := make(map[string]struct{}, len(schemas.Tools))
	for name := range schemas.Tools {
		out[name] = struct{}{}
	}
	return out
}

func loadProfileToolNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool-profiles", err)
	out := make(map[string]struct{})
	for _, p := range profiles {
		for name, allowed := range p.Tools {
			if allowed {
				out[name] = struct{}{}
			}
		}
		for _, name := range p.DenyTools {
			out[name] = struct{}{}
		}
	}
	return out
}

func loadNativeManifestNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	names := loadNativeToolsManifestFlat(t)
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func TestToolRegistryTriStateSchemasMatchNativeManifest(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas := loadToolSchemaNames(t, root)
	native := loadNativeManifestNames(t, root)

	var missingSchema []string
	for name := range native {
		if _, ok := schemas[name]; !ok {
			missingSchema = append(missingSchema, name)
		}
	}
	contractcheck.FailViolations(t, "native-tools.yaml entries missing from tools/schemas", missingSchema)

	var orphanSchema []string
	for name := range schemas {
		if _, ok := native[name]; ok {
			continue
		}
		if _, exempt := toolsNativeSchemaOnly[name]; exempt {
			continue
		}
		orphanSchema = append(orphanSchema, name)
	}
	contractcheck.FailViolations(t, "tools/schemas entries missing from native-tools.yaml (not exempt)", orphanSchema)
}

func TestToolRegistryTriStateHandlersMatchSchemas(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	registered := tools.RegisteredToolSet(reg)
	schemas := loadToolSchemaNames(t, root)

	var missingHandler []string
	for name := range schemas {
		if _, ok := registered[name]; !ok {
			if _, exempt := toolsSchemaExempt[name]; exempt {
				continue
			}
			missingHandler = append(missingHandler, name)
		}
	}
	contractcheck.FailViolations(t, "tools/schemas tools missing registered handlers", missingHandler)
}

func TestToolRegistryTriStateProfilesReferenceRegisteredTools(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	registered := tools.RegisteredToolSet(reg)
	profiles := loadProfileToolNames(t, contractcheck.RepoRoot(t))

	var violations []string
	for name := range profiles {
		if strings.Contains(name, "*") {
			continue
		}
		// mcp_* handlers register at runtime via MCP server sync (SyncTools),
		// never in the serve-boot mirror.
		if strings.HasPrefix(name, "mcp_") {
			continue
		}
		if !registered[name] {
			violations = append(violations, "tool-profiles reference "+name+" but serve registry has no handler")
		}
	}
	contractcheck.FailViolations(t, "tool-profiles reference unregistered tools", violations)
}

func TestToolRegistryTriStateCoordinatorProfileToolsHaveSchemas(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas := loadToolSchemaNames(t, root)
	profiles := loadProfileToolNames(t, root)

	var missing []string
	for name := range profiles {
		if strings.Contains(name, "*") {
			continue
		}
		// mcp_* schemas come from the MCP server at sync time, not tools/schemas.
		if strings.HasPrefix(name, "mcp_") {
			continue
		}
		if _, ok := schemas[name]; ok {
			continue
		}
		if _, exempt := toolsProfileSchemaExempt[name]; exempt {
			continue
		}
		missing = append(missing, name)
	}
	contractcheck.FailViolations(t, "tool-profiles tools missing tools/schemas (not exempt)", missing)
}

func TestToolRegistryTriStateExemptMapsStale(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	registered := tools.RegisteredToolSet(reg)
	schemas := loadToolSchemaNames(t, root)
	profiles := loadProfileToolNames(t, root)
	native := loadNativeManifestNames(t, root)

	all := make(map[string]struct{})
	for k := range registered {
		all[k] = struct{}{}
	}
	for k := range schemas {
		all[k] = struct{}{}
	}
	for k := range profiles {
		all[k] = struct{}{}
	}
	for k := range native {
		all[k] = struct{}{}
	}

	checkStale := func(label string, exempt map[string]string) {
		var stale []string
		for code := range exempt {
			if _, ok := all[code]; !ok {
				stale = append(stale, code)
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			t.Fatalf("stale %s entries: %v", label, stale)
		}
	}
	checkStale("toolsSchemaExempt", toolsSchemaExempt)
	checkStale("toolsNativeSchemaOnly", toolsNativeSchemaOnly)
	checkStale("toolsProfileSchemaExempt", toolsProfileSchemaExempt)
}

func TestAgentToolProfilesYAMLShape(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "agent-tool-profiles.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read agent-tool-profiles.yaml", err)
	var doc struct {
		Profiles map[string]any `yaml:"profiles"`
	}
	contractcheck.FailErr(t, "parse agent-tool-profiles.yaml", yaml.Unmarshal(data, &doc))
	if len(doc.Profiles) == 0 {
		t.Fatal("agent-tool-profiles.yaml must declare at least coordinator profile")
	}
}
