package catalogload

import (
	"path"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/test/contract/laws"
)

const (
	packRoot   = "lycaon/config/packs/painted-wolf"
	schemasDir = packRoot + "/platform/tools/schemas"
)

// policyHandle finds a name a policy unit cites as an evidence handle.
var policyHandle = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.:-])([a-z][a-z0-9_]*)#`)

var pongoInclude = regexp.MustCompile(`\{%\s*include\s+"([^"]+)"`)

var toolCatalog = []laws.Rule{{
	// Every native tool has a schema, and every schema a native tool,
	// operation or serve-boot registration.
	Name:    "tool-schemas-match-the-native-manifest",
	Anchors: []laws.Anchor{laws.Dir(schemasDir), laws.Func("internal/nativemanifest", "Load")},
	Exempt:  schemaOnlyTools,
	Check: func(r *laws.Run) {
		schemas, cfg := toolSchemas(r), nativeManifest(r)
		native := cfg.AllTools()
		r.Expect(len(native), "native tools")
		for _, name := range native {
			if _, ok := schemas.Tools[name]; !ok {
				r.Report("native tool %s has no schema in tools/schemas", name)
			}
		}
		for name := range schemas.Tools {
			if !contains(native, name) && !cfg.HasOperation(name) && !r.Exempt(name) {
				r.Report("tools/schemas entry %s is in no native manifest", name)
			}
		}
	},
}, {
	// Tools a profile names have a static schema, or one built at serve boot.
	Name:    "profile-tools-have-schemas",
	Anchors: []laws.Anchor{laws.Dir(schemasDir), laws.Func("internal/sandbox", "LoadToolProfiles")},
	Check: func(r *laws.Run) {
		schemas := toolSchemas(r)
		profiles, err := sandbox.LoadToolProfiles()
		r.Must(err)
		r.Expect(len(profiles), "tool profiles")
		names := map[string]bool{}
		for _, p := range profiles {
			for name, allowed := range p.Tools {
				if allowed {
					names[name] = true
				}
			}
			for _, name := range p.DenyTools {
				names[name] = true
			}
		}
		for name := range names {
			if strings.Contains(name, "*") || strings.HasPrefix(name, "mcp_") || toolcontract.IsCommandOperation(name) {
				continue
			}
			if _, ok := schemas.Tools[name]; !ok {
				r.Report("tool profiles name %s, which has no tools/schemas entry", name)
			}
		}
	},
}, {
	// Policy copy renders per occurrence from facts that do not carry the
	// reader's tools, so a handle it cites is a declared evidence kind.
	Name:    "policy-copy-names-declared-evidence-handles",
	Anchors: []laws.Anchor{laws.Func("internal/evidence", "LoadBinding")},
	Check: func(r *laws.Run) {
		binding, err := evidence.LoadBinding()
		r.Must(err)
		kinds := binding.KindShapeMap()
		cited := 0
		files := append(r.Glob("lycaon/config/packs/*/*/policy/*.yaml"), r.Glob("lycaon/config/packs/*/*/host/next-steps/*.yaml")...)
		for _, rel := range files {
			for _, m := range policyHandle.FindAllStringSubmatch(r.Text(rel), -1) {
				cited++
				if _, known := kinds[m[1]]; !known && binding.ToolKind(m[1]) != "" {
					r.Report("%s names tool %s# as an evidence handle; its kind is %s#", rel, m[1], binding.ToolKind(m[1]))
				}
			}
		}
		r.Expect(cited, "evidence handles in policy copy")
	},
}, {
	// A partial with no include site renders nowhere.
	Name:    "prompt-partials-are-reachable",
	Anchors: []laws.Anchor{laws.Dir(packRoot)},
	Check: func(r *laws.Run) {
		templates := r.Tree(packRoot, ".md")
		included := map[string]bool{}
		for _, f := range templates {
			for _, m := range pongoInclude.FindAllStringSubmatch(f.Text(), -1) {
				included[m[1]] = true
			}
		}
		partials := 0
		for _, f := range templates {
			if path.Base(path.Dir(f.Rel)) != "partials" {
				continue
			}
			partials++
			if !included["partials/"+path.Base(f.Rel)] {
				r.Report("%s/%s has no include site", packRoot, f.Rel)
			}
		}
		r.Expect(partials, "prompt partials")
	},
}}

func toolSchemas(r *laws.Run) *toolschema.Config {
	set, err := toolschema.LoadSchemaDir(r.Path(schemasDir))
	r.Must(err)
	r.Expect(len(set.Tools), "tool schemas")
	return set
}

func nativeManifest(r *laws.Run) nativemanifest.Config {
	cfg, err := nativemanifest.Load()
	r.Must(err)
	return cfg
}

func contains(items []string, item string) bool {
	for _, x := range items {
		if x == item {
			return true
		}
	}
	return false
}

// schemaOnlyTools have schemas but register at serve boot, outside the
// native manifests.
var schemaOnlyTools = []laws.Exemption{
	{Key: "command", Reason: "host shell; registered at serve boot, not native manifest"},
	{Key: "command_output", Reason: "background command companion; registered at serve boot"},
	{Key: "command_stop", Reason: "background command companion; registered at serve boot"},
	{Key: "held_result", Reason: "held call companion; registered at serve boot"},
	{Key: "held_stop", Reason: "held call companion; registered at serve boot"},
	{Key: "fetch_url", Reason: "web research; registered at serve boot"},
	{Key: "request_decision", Reason: "worker decision escalation; registered at serve boot"},
	{Key: "message_coordinator", Reason: "worker note to its coordinator; registered at serve boot"},
	{Key: "record_finding", Reason: "findings feed writer; registered at serve boot"},
	{Key: "scan_pack", Reason: "scan orchestration; schema built from the live scanner registry at serve boot"},
	{Key: "scan_list", Reason: "native scan drill-down; schema registered at serve boot via RegisterScanTools"},
	{Key: "scan_summary", Reason: "native scan drill-down; schema registered at serve boot via RegisterScanTools"},
	{Key: "scan_query", Reason: "native scan drill-down; schema registered at serve boot via RegisterScanTools"},
	{Key: "scan_compare", Reason: "native scan compare; schema registered at serve boot via RegisterScanTools"},
	{Key: "update_progress", Reason: "coordinator progress writer; registered at serve boot"},
	{Key: "web_search", Reason: "web research; registered at serve boot"},
	{Key: "promote_overlay", Reason: "coordinator overlay surface; registered at serve boot"},
	{Key: "reject_overlay", Reason: "coordinator overlay surface; registered at serve boot"},
	{Key: "preview_overlay", Reason: "coordinator overlay surface; registered at serve boot"},
	{Key: "wait", Reason: "coordinator wait tool; registered at serve boot"},
	{Key: "task", Reason: "worker spawn tool; registered at serve boot"},
	{Key: "worker_cancel", Reason: "coordinator worker tool; registered at serve boot"},
	{Key: "answer_decision", Reason: "coordinator worker decision tool; registered at serve boot"},
	{Key: "instruct_worker", Reason: "coordinator worker tool; registered at serve boot"},
	{Key: "shelve_overlay", Reason: "coordinator overlay surface; registered at serve boot"},
	{Key: "worker_history", Reason: "coordinator worker tool; registered at serve boot"},
	{Key: "plan_from_blueprint", Reason: "blueprint planning tool; registered at serve boot"},
	{Key: "pack_board", Reason: "board tool; registered at serve boot"},
	{Key: "read_findings", Reason: "findings tool; registered at serve boot"},
	{Key: "read_review", Reason: "board review tool; registered at serve boot"},
	{Key: "parse_evaluation", Reason: "parser tool; registered at serve boot"},
	{Key: "parse_extract_json", Reason: "parser tool; registered at serve boot"},
	{Key: "parse_validate", Reason: "parser tool; registered at serve boot"},
	{Key: "plan_append_review_evidence", Reason: "plan review tool; registered at serve boot"},
	{Key: "workflow_advance", Reason: "workflow tool; registered at serve boot"},
	{Key: "workflow_transition", Reason: "workflow tool; registered at serve boot"},
	{Key: "fanout_plan", Reason: "workflow tool; registered at serve boot"},
	{Key: "submit_verdict", Reason: "workflow tool; registered at serve boot"},
	{Key: "workflow_catalog_summaries", Reason: "workflow tool; registered at serve boot"},
	{Key: "workflow_compose", Reason: "workflow tool; registered at serve boot"},
	{Key: "workflow_compose_from_template", Reason: "workflow tool; registered at serve boot"},
	{Key: "workflow_persist", Reason: "workflow tool; registered at serve boot"},
	{Key: "workflow_user_feedback", Reason: "workflow tool; registered at serve boot"},
	{Key: "ask_user", Reason: "coordinator ask_user; registered at serve boot"},
	{Key: "state_start", Reason: "workflow state tool; registered at serve boot"},
	{Key: "state_close", Reason: "workflow state tool; registered at serve boot"},
	{Key: "state_query", Reason: "workflow state tool; registered at serve boot"},
	{Key: "state_update", Reason: "workflow state tool; registered at serve boot"},
	{Key: "handoff_health", Reason: "sibling worker coordination; registered at serve boot"},
	{Key: "handoff_init", Reason: "sibling worker coordination; registered at serve boot"},
	{Key: "handoff_release", Reason: "sibling worker coordination; registered at serve boot"},
	{Key: "handoff_release_all", Reason: "sibling worker coordination; registered at serve boot"},
	{Key: "handoff_reserve", Reason: "sibling worker coordination; registered at serve boot"},
}
