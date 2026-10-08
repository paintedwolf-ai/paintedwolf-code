package oar

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// FactTier classifies a fact for portability.
type FactTier string

const (
	FactTierCore     FactTier = "core"     // every conforming host provides these
	FactTierStandard FactTier = "standard" // hosts implementing a named profile
	FactTierHost     FactTier = "host"     // published under the host namespace
)

// Standard profile names. A profile is all-or-nothing: a host either provides
// every member or declares none of them ([OAR-FACT-16]).
const (
	ProfileTool              = "tool"
	ProfileSession           = "session"
	ProfileFilesystem        = "filesystem"
	ProfileContentProvenance = "content-provenance"
	ProfileContent           = "content"
	ProfileSecrets           = "secrets"
	ProfileMCP               = "mcp"
)

// factDecl is one typed fact in the closed environment.
type factDecl struct {
	name    string
	typ     oarcore.FactType
	tier    FactTier
	profile string // standard profile name; empty unless tier is standard
}

// factDecls is the closed fact set in documentation order.
var factDecls = []factDecl{
	// Core facts defined by OAR-FACT-15.
	{"anchor", oarcore.TypeString, FactTierCore, ""},
	{"fire_count", oarcore.TypeInt, FactTierCore, ""},

	// Standard profile facts defined by OAR-FACT-16.
	{"permission_profile", oarcore.TypeString, FactTierStandard, ProfileTool},
	{"principal", oarcore.TypeString, FactTierStandard, ProfileSession},
	{"principal_roles", oarcore.TypeListString, FactTierStandard, ProfileSession},
	{"content_length", oarcore.TypeInt, FactTierStandard, ProfileContent},
	{"content_roles", oarcore.TypeListString, FactTierStandard, ProfileContentProvenance},
	{"content_origins", oarcore.TypeListString, FactTierStandard, ProfileContentProvenance},
	{"content_authorities", oarcore.TypeListString, FactTierStandard, ProfileContentProvenance},
	{"content_trust_tiers", oarcore.TypeListString, FactTierStandard, ProfileContentProvenance},
	{"content_sources", oarcore.TypeListString, FactTierStandard, ProfileContentProvenance},
	{"content_segment_count", oarcore.TypeInt, FactTierStandard, ProfileContentProvenance},
	{"content_contains_untrusted", oarcore.TypeBool, FactTierStandard, ProfileContentProvenance},

	{"tool", oarcore.TypeString, FactTierStandard, ProfileTool},
	{"tool_args", oarcore.TypeMap, FactTierStandard, ProfileTool},
	{"last_assistant", oarcore.TypeString, FactTierHost, ""},
	{"turn_tools", oarcore.TypeListString, FactTierHost, ""},
	{"session_posture", oarcore.TypeString, FactTierStandard, ProfileSession},
	{"surface", oarcore.TypeString, FactTierHost, ""},
	{"profile", oarcore.TypeString, FactTierHost, ""},
	{"phase", oarcore.TypeString, FactTierHost, ""},
	{"workers_idle", oarcore.TypeBool, FactTierHost, ""},
	{"worker_spawn_blocked", oarcore.TypeBool, FactTierHost, ""},
	{"active_worker_count", oarcore.TypeInt, FactTierHost, ""},
	{"pending_overlay_promote", oarcore.TypeBool, FactTierHost, ""},
	{"overlay_state", oarcore.TypeString, FactTierHost, ""},
	{"surface_may_finish", oarcore.TypeBool, FactTierHost, ""},
	{"is_host_cycle_turn", oarcore.TypeBool, FactTierHost, ""},
	{"batch_phase", oarcore.TypeString, FactTierHost, ""},
	{"batch_closed", oarcore.TypeBool, FactTierHost, ""},
	{"progress_open_items", oarcore.TypeInt, FactTierHost, ""},
	{"progress_has_open_steps", oarcore.TypeBool, FactTierHost, ""},
	{"has_completion_report", oarcore.TypeBool, FactTierHost, ""},
	{"task_envelope_echo", oarcore.TypeBool, FactTierHost, ""},
	{"closeout_surface", oarcore.TypeBool, FactTierHost, ""},
	{"progress_reconcile_needed", oarcore.TypeBool, FactTierHost, ""},
	{"verify_required", oarcore.TypeBool, FactTierHost, ""},
	{"verifier_pass", oarcore.TypeBool, FactTierHost, ""},
	{"synthesis_wrapup_tool_forbidden", oarcore.TypeBool, FactTierHost, ""},
	{"progress_closure_armed", oarcore.TypeBool, FactTierHost, ""},
	{"progress_gated_tool", oarcore.TypeBool, FactTierHost, ""},
	{"progress_missing", oarcore.TypeBool, FactTierHost, ""},
	{"review_loop_active", oarcore.TypeBool, FactTierHost, ""},
	{"path_is_worker_branch", oarcore.TypeBool, FactTierHost, ""},
	{"verify_has_command", oarcore.TypeBool, FactTierHost, ""},
	{"verify_declared", oarcore.TypeBool, FactTierHost, ""},
	{"progress_closed_beyond_baseline", oarcore.TypeBool, FactTierHost, ""},
	{"progress_reconciled_since_arm", oarcore.TypeBool, FactTierHost, ""},
	{"pending_user_input", oarcore.TypeBool, FactTierHost, ""},
	{"stub_valid", oarcore.TypeBool, FactTierHost, ""},
	{"tool_allowed_for_profile", oarcore.TypeBool, FactTierHost, ""},
	{"habit_redirect_match", oarcore.TypeString, FactTierHost, ""},
	{"write_roots", oarcore.TypeListString, FactTierHost, ""},
	{"pattern_parse_ok", oarcore.TypeBool, FactTierHost, ""},
	{"arg_validation_errors", oarcore.TypeListString, FactTierStandard, ProfileTool},
	{"arg_validation_reason", oarcore.TypeString, FactTierStandard, ProfileTool},
	{"arg_validation_field", oarcore.TypeString, FactTierStandard, ProfileTool},
	{"action_host_resources", oarcore.TypeListString, FactTierHost, ""},
	{"action_host_resource_denials", oarcore.TypeListString, FactTierHost, ""},
	{"confine_applied", oarcore.TypeBool, FactTierHost, ""},
	{"network_mode", oarcore.TypeString, FactTierHost, ""},
	{"denial_subject", oarcore.TypeString, FactTierHost, ""},
	{"confine_signals", oarcore.TypeListString, FactTierHost, ""},
	{"failed_stages", oarcore.TypeListString, FactTierHost, ""},
	{"process_running", oarcore.TypeBool, FactTierHost, ""},
	{"sandbox_refusals", oarcore.TypeListString, FactTierHost, ""},
	{"refused_write_paths", oarcore.TypeListString, FactTierHost, ""},
	{"refused_write_grants", oarcore.TypeListString, FactTierHost, ""},
	{"refused_read_paths", oarcore.TypeListString, FactTierHost, ""},
	{"refused_read_grants", oarcore.TypeListString, FactTierHost, ""},
	{"receiving_tool_summarize", oarcore.TypeBool, FactTierHost, ""},
	{"refused_terminal_read_paths", oarcore.TypeListString, FactTierHost, ""},
	{"refused_terminal_write_paths", oarcore.TypeListString, FactTierHost, ""},
	{"kernel_refusal_silence", oarcore.TypeBool, FactTierHost, ""},
	{"refused_socket_paths", oarcore.TypeListString, FactTierHost, ""},
	{"refused_connect_ports", oarcore.TypeListString, FactTierHost, ""},
	{"refused_listen_ports", oarcore.TypeListString, FactTierHost, ""},
	{"refused_signals", oarcore.TypeListString, FactTierHost, ""},
	{"unsandboxed_refusals", oarcore.TypeListString, FactTierHost, ""},
	{"worktree_stale_paths", oarcore.TypeListString, FactTierHost, ""},
	{"worktree_leftover_paths", oarcore.TypeListString, FactTierHost, ""},
	{"worktree_conflict_paths", oarcore.TypeListString, FactTierHost, ""},
	{"mode_bits", oarcore.TypeString, FactTierHost, ""},
	{"tool_args_fingerprint", oarcore.TypeString, FactTierStandard, ProfileTool},
	{"posture_unresolved", oarcore.TypeBool, FactTierHost, ""},
	{"high_risk_tool", oarcore.TypeBool, FactTierHost, ""},
	{"tool_is_state", oarcore.TypeBool, FactTierHost, ""},
	{"tool_is_delegation", oarcore.TypeBool, FactTierHost, ""},
	{"tool_is_task", oarcore.TypeBool, FactTierHost, ""},
	{"tool_payload_chunkable", oarcore.TypeBool, FactTierHost, ""},
	{"tool_is_handoff", oarcore.TypeBool, FactTierHost, ""},
	{"pack_runner_task", oarcore.TypeBool, FactTierHost, ""},
	{"agent_is_plan_writer", oarcore.TypeBool, FactTierHost, ""},
	{"disallowed_agent", oarcore.TypeBool, FactTierHost, ""},
	{"plan_awaiting_approval", oarcore.TypeBool, FactTierHost, ""},
	{"unobserved_cited_paths", oarcore.TypeListString, FactTierHost, ""},
	{"unobserved_cited_urls", oarcore.TypeListString, FactTierHost, ""},
	{"unobserved_cited_handles", oarcore.TypeListString, FactTierHost, ""},
	{"citation_fields_present", oarcore.TypeBool, FactTierHost, ""},
	{"claims_completion", oarcore.TypeBool, FactTierHost, ""},
	{"has_matching_ledger_job", oarcore.TypeBool, FactTierHost, ""},
	{"ledger_criteria_met", oarcore.TypeBool, FactTierHost, ""},
	{"worker_summary_present", oarcore.TypeBool, FactTierHost, ""},
	{"worker_artifact_present", oarcore.TypeBool, FactTierHost, ""},
	{"worker_artifact_measured", oarcore.TypeBool, FactTierHost, ""},
	{"files_touched", oarcore.TypeListString, FactTierHost, ""},
	{"summary_length", oarcore.TypeInt, FactTierHost, ""},
	{"repeat_count", oarcore.TypeInt, FactTierHost, ""},
	{"fruitless_search_run", oarcore.TypeInt, FactTierHost, ""},
	{"breaker_count", oarcore.TypeInt, FactTierCore, ""},
	{"same_code_reject_run", oarcore.TypeInt, FactTierHost, ""},
	{"code_reject_responses", oarcore.TypeInt, FactTierHost, ""},
	{"deferred_unactivated", oarcore.TypeInt, FactTierHost, ""},
	{"worker_attempted_mutation", oarcore.TypeBool, FactTierHost, ""},
	{"batch_ready_ignoring_progress", oarcore.TypeBool, FactTierHost, ""},
	{"synthesis_delay_count", oarcore.TypeInt, FactTierHost, ""},
	{"review_verdict_gate_open", oarcore.TypeBool, FactTierHost, ""},
	{"verdict_delay_count", oarcore.TypeInt, FactTierHost, ""},
	{"closeout_gates_open", oarcore.TypeBool, FactTierHost, ""},
	{"closeout_gate_open_leaves", oarcore.TypeString, FactTierHost, ""},
	{"closeout_gate_delay_count", oarcore.TypeInt, FactTierHost, ""},
	{"workflow_report_phase_pending", oarcore.TypeBool, FactTierHost, ""},
	{"phase_obligation_pending", oarcore.TypeBool, FactTierHost, ""},
	{"phase_obligation_kinds", oarcore.TypeString, FactTierHost, ""},
	{"scope_mode", oarcore.TypeString, FactTierHost, ""},
	{"profile_mutation_capable", oarcore.TypeBool, FactTierHost, ""},
	{"base_overlay_id", oarcore.TypeString, FactTierHost, ""},
	{"base_overlay_resolves", oarcore.TypeBool, FactTierHost, ""},
	{"base_overlay_checked", oarcore.TypeBool, FactTierHost, ""},
	{"base_overlay_pending", oarcore.TypeBool, FactTierHost, ""},
	{"active_read_count", oarcore.TypeInt, FactTierHost, ""},
	{"active_write_count", oarcore.TypeInt, FactTierHost, ""},
	{"max_workers", oarcore.TypeInt, FactTierHost, ""},
	{"max_read_workers", oarcore.TypeInt, FactTierHost, ""},
	{"max_write_workers", oarcore.TypeInt, FactTierHost, ""},
	{"citation_unverifiable", oarcore.TypeBool, FactTierHost, ""},
	{"scout_survey_evidence_present", oarcore.TypeBool, FactTierHost, ""},
	{"surface_claim_ungrounded", oarcore.TypeBool, FactTierHost, ""},
	{"page_measure_ungrounded", oarcore.TypeBool, FactTierHost, ""},
	{"agent_is_scout", oarcore.TypeBool, FactTierHost, ""},
	{"agent_is_implementer", oarcore.TypeBool, FactTierHost, ""},
	{"profile_surveys_project_tree", oarcore.TypeBool, FactTierHost, ""},
	{"profile_fetches_urls", oarcore.TypeBool, FactTierHost, ""},
	{"repo_known_empty", oarcore.TypeBool, FactTierHost, ""},
	{"last_audit_ungrounded", oarcore.TypeBool, FactTierHost, ""},
	{"grounding_escalated", oarcore.TypeBool, FactTierHost, ""},
	{"command_not_argv", oarcore.TypeBool, FactTierHost, ""},
	{"is_directory", oarcore.TypeBool, FactTierStandard, ProfileFilesystem},
	{"not_found", oarcore.TypeBool, FactTierStandard, ProfileFilesystem},
	{"path_denied", oarcore.TypeBool, FactTierStandard, ProfileFilesystem},
	{"bulk_denied", oarcore.TypeBool, FactTierHost, ""},
	{"binary_denied", oarcore.TypeBool, FactTierHost, ""},
	{"mode_denied", oarcore.TypeBool, FactTierHost, ""},
	{"path_escape", oarcore.TypeBool, FactTierHost, ""},
	{"beyond_eof", oarcore.TypeBool, FactTierHost, ""},
	{"not_running", oarcore.TypeBool, FactTierHost, ""},
	{"unsupported", oarcore.TypeBool, FactTierHost, ""},
	{"resource_limit", oarcore.TypeBool, FactTierHost, ""},
	{"conflict", oarcore.TypeBool, FactTierHost, ""},
	{"path_required", oarcore.TypeBool, FactTierHost, ""},
	{"id_required", oarcore.TypeBool, FactTierHost, ""},
	{"policy_denied", oarcore.TypeBool, FactTierStandard, ProfileTool},
	{"unknown_target", oarcore.TypeBool, FactTierHost, ""},
	{"missing", oarcore.TypeBool, FactTierHost, ""},
	{"forbidden", oarcore.TypeBool, FactTierHost, ""},
	{"selector_empty", oarcore.TypeBool, FactTierHost, ""},
	{"selector_ambiguous", oarcore.TypeBool, FactTierHost, ""},
	{"reject_observation", oarcore.TypeString, FactTierHost, ""},
	{"rejection_code", oarcore.TypeString, FactTierHost, ""},
	{"secret_matches", oarcore.TypeListMap, FactTierStandard, ProfileSecrets},
	{"recent_tool_names", oarcore.TypeListString, FactTierHost, ""},
	{"mcp_provider_id", oarcore.TypeString, FactTierStandard, ProfileMCP},
	{"mcp_tool_name", oarcore.TypeString, FactTierStandard, ProfileMCP},
	{"mcp_qualified_tool", oarcore.TypeString, FactTierStandard, ProfileMCP},
	{"mcp_provider_configured", oarcore.TypeBool, FactTierStandard, ProfileMCP},
	{"mcp_provider_enabled", oarcore.TypeBool, FactTierStandard, ProfileMCP},
	{"mcp_call_ok", oarcore.TypeBool, FactTierStandard, ProfileMCP},
	{"mcp_error_code", oarcore.TypeString, FactTierStandard, ProfileMCP},
	{"mcp_schema_matched", oarcore.TypeBool, FactTierStandard, ProfileMCP},
	{"credential_length", oarcore.TypeInt, FactTierHost, ""},
	{"credential_distinct_words", oarcore.TypeInt, FactTierHost, ""},
	{"credential_longest_run", oarcore.TypeInt, FactTierHost, ""},
	{"credential_run_classes", oarcore.TypeInt, FactTierHost, ""},
	{"credential_fingerprint", oarcore.TypeString, FactTierHost, ""},
	{"credential_harvested", oarcore.TypeBool, FactTierHost, ""},
	{"credential_ignored", oarcore.TypeBool, FactTierHost, ""},
	{"editorconfig_mismatch", oarcore.TypeBool, FactTierHost, ""},
	{"syntax_check_overridden", oarcore.TypeBool, FactTierHost, ""},
	{"source_analysis_unavailable", oarcore.TypeBool, FactTierHost, ""},
	{"http_request_web_page", oarcore.TypeBool, FactTierHost, ""},
}

// observationFn is one parameterized observation function.
type observationFn struct {
	name    string
	ret     oarcore.FactType
	tier    FactTier
	profile string // standard profile name; empty unless tier is standard
	bind    func(h *evalHolder, s string) any
}

var observationFns = []observationFn{
	// Counter references resolve during rule-set validation ([OAR-FIRE-11]).
	{"fire_count_of", oarcore.TypeInt, FactTierCore, "", nil},
	{"breaker_count_of", oarcore.TypeInt, FactTierCore, "", nil},
	{"path_outside_scope", oarcore.TypeBool, FactTierStandard, ProfileFilesystem, func(h *evalHolder, s string) any {
		return EvalPathOutsideScope(h.get(), s)
	}},
	{"source_includes", oarcore.TypeBool, FactTierHost, "", func(h *evalHolder, s string) any {
		return EvalSourceIncludes(h.get(), s)
	}},
	// Typed accessors expose heterogeneous tool arguments ([OAR-EXPR-19]).
	{"tool_arg_string", oarcore.TypeString, FactTierStandard, ProfileTool, func(h *evalHolder, s string) any {
		return EvalToolArgString(h.get(), s)
	}},
	{"tool_arg_int", oarcore.TypeInt, FactTierStandard, ProfileTool, func(h *evalHolder, s string) any {
		return EvalToolArgInt(h.get(), s)
	}},
	{"tool_arg_bool", oarcore.TypeBool, FactTierStandard, ProfileTool, func(h *evalHolder, s string) any {
		return EvalToolArgBool(h.get(), s)
	}},
	// The _for suffix distinguishes functions from facts ([OAR-FACT-2]).
	{"mcp_provider_configured_for", oarcore.TypeBool, FactTierStandard, ProfileMCP, func(h *evalHolder, s string) any {
		return EvalMCPProviderConfigured(h.get(), s)
	}},
	{"mcp_provider_enabled_for", oarcore.TypeBool, FactTierStandard, ProfileMCP, func(h *evalHolder, s string) any {
		return EvalMCPProviderEnabled(h.get(), s)
	}},
	{"mcp_has_field", oarcore.TypeBool, FactTierStandard, ProfileMCP, func(h *evalHolder, s string) any {
		return EvalMCPHasField(h.get(), s)
	}},
	{"mcp_field_bool", oarcore.TypeBool, FactTierStandard, ProfileMCP, func(h *evalHolder, s string) any {
		return EvalMCPFieldBool(h.get(), s)
	}},
	{"mcp_field_string", oarcore.TypeString, FactTierStandard, ProfileMCP, func(h *evalHolder, s string) any {
		return EvalMCPFieldString(h.get(), s)
	}},
	{"mcp_field_int", oarcore.TypeInt, FactTierStandard, ProfileMCP, func(h *evalHolder, s string) any {
		return EvalMCPFieldInt(h.get(), s)
	}},
	{"host_resource_status_for", oarcore.TypeString, FactTierHost, "", func(h *evalHolder, s string) any {
		return EvalHostResourceStatus(h.get(), s)
	}},
	{"host_resource_policy_for", oarcore.TypeString, FactTierHost, "", func(h *evalHolder, s string) any {
		return EvalHostResourcePolicy(h.get(), s)
	}},
}

// Fact classes identify counter, detector, lazy-provider, and structural sources.
var engineCounterFacts = map[string]struct{}{
	"paintedwolf.repeat_count": {}, "breaker_count": {}, "fire_count": {},
	"paintedwolf.same_code_reject_run": {}, "paintedwolf.fruitless_search_run": {},
	"paintedwolf.code_reject_responses": {},
}

var detectorFacts = map[string]struct{}{
	"secret_matches": {},
}

// FactInfo is the docs-facing projection of one catalogue fact.
type FactInfo struct {
	Name          string
	Type          string   // OAR fact type
	Class         string   // structural | derived | engine_counter | detector
	Tier          FactTier // core | standard | host
	Profile       string   // standard profile name; empty unless Tier is standard
	PublishedName string   // qualified host fact name; empty for portable facts
}

// FunctionInfo is the docs-facing projection of one observation function.
type FunctionInfo struct {
	Name          string
	Signature     string // e.g. (string) -> bool
	Tier          FactTier
	Profile       string
	PublishedName string
}

// FactCatalogue returns facts in declaration order.
func FactCatalogue() []FactInfo {
	out := make([]FactInfo, 0, len(factDecls))
	for _, d := range factDecls {
		out = append(out, FactInfo{
			Name:          d.name,
			Type:          factTypeString(d.typ),
			Class:         factClass(d.name),
			Tier:          d.tier,
			Profile:       d.profile,
			PublishedName: publishedHostName(d.name, d.tier),
		})
	}
	return out
}

// ObservationFunctionCatalogue returns the parameterized observation
// functions in declaration order, for docs codegen.
func ObservationFunctionCatalogue() []FunctionInfo {
	out := make([]FunctionInfo, 0, len(observationFns))
	for _, f := range observationFns {
		out = append(out, FunctionInfo{
			Name:          f.name,
			Signature:     "(string) -> " + factTypeString(f.ret),
			Tier:          f.tier,
			Profile:       f.profile,
			PublishedName: publishedHostName(f.name, f.tier),
		})
	}
	return out
}

func factClass(name string) string {
	// expensiveFacts and engineCounterFacts are keyed by the published name,
	// which is the name a rule writes ([OAR-FACT-18]).
	name = PublishedFactName(name)
	if _, ok := engineCounterFacts[name]; ok {
		return "engine_counter"
	}
	if _, ok := detectorFacts[name]; ok {
		return "detector"
	}
	if _, ok := expensiveFacts[name]; ok {
		return "derived"
	}
	return "structural"
}

func factTypeString(t oarcore.FactType) string { return string(t) }

func publishedHostName(name string, tier FactTier) string {
	if tier != FactTierHost {
		return ""
	}
	return oarcopy.HostFactNamespace + "." + name
}

// requiredCoreFacts lists the section 5.3 core facts.
var requiredCoreFacts = []string{
	"anchor",
	"fire_count",
	"breaker_count",
}

// requiredCoreFunctions is the normative core observation-function set.
var requiredCoreFunctions = []string{
	"fire_count_of",
	"breaker_count_of",
}

// standardProfileFacts is each named standard profile's complete membership,
// from the standard's vocabulary.yaml. A host provides every member of a
// profile it claims, or none of them ([OAR-FACT-16]).
var standardProfileFacts = map[string][]string{
	ProfileTool: {
		"tool", "tool_args", "tool_args_fingerprint", "arg_validation_errors",
		"arg_validation_reason", "arg_validation_field",
		"permission_profile", "policy_denied",
		"tool_arg_string", "tool_arg_int", "tool_arg_bool",
	},
	ProfileSession:    {"session_posture", "principal", "principal_roles"},
	ProfileFilesystem: {"is_directory", "not_found", "path_denied", "path_outside_scope"},
	ProfileSecrets:    {"secret_matches"},
	ProfileContent:    {"content_length"},
	ProfileContentProvenance: {
		"content_roles", "content_origins", "content_authorities", "content_trust_tiers",
		"content_sources", "content_segment_count", "content_contains_untrusted",
	},
	ProfileMCP: {
		"mcp_provider_id", "mcp_tool_name", "mcp_qualified_tool", "mcp_provider_configured",
		"mcp_provider_enabled", "mcp_call_ok", "mcp_error_code", "mcp_schema_matched",
		"mcp_provider_configured_for", "mcp_provider_enabled_for",
		"mcp_has_field", "mcp_field_bool", "mcp_field_string", "mcp_field_int",
	},
}

// factTiers indexes every declared fact and function by tier, so the published
// name and the portability verdict are both computable from one table.
func factTiers() map[string]FactTier {
	out := make(map[string]FactTier, len(factDecls)+len(observationFns))
	for _, d := range factDecls {
		out[d.name] = d.tier
	}
	for _, f := range observationFns {
		out[f.name] = f.tier
	}
	return out
}

// PublishedFactName namespaces host facts and leaves standard names bare ([OAR-FACT-18]).
func PublishedFactName(name string) string {
	if published := publishedHostName(name, factTiers()[name]); published != "" {
		return published
	}
	return name
}

// BareFactName is the inverse of PublishedFactName.
func BareFactName(published string) string {
	return strings.TrimPrefix(published, oarcopy.HostFactNamespace+".")
}

// ValidateCatalogueTiers errors when a declaration is untiered, a locked core
// name is absent from the live catalogue, or a claimed standard profile is only
// partly provided.
func ValidateCatalogueTiers() error {
	facts := map[string]FactTier{}
	for _, d := range factDecls {
		switch d.tier {
		case FactTierCore, FactTierStandard, FactTierHost:
		default:
			return fmt.Errorf("fact %q has no tier", d.name)
		}
		if (d.tier == FactTierStandard) != (d.profile != "") {
			return fmt.Errorf("fact %q tier %q and profile %q disagree", d.name, d.tier, d.profile)
		}
		facts[d.name] = d.tier
	}
	fns := map[string]FactTier{}
	for _, f := range observationFns {
		switch f.tier {
		case FactTierCore, FactTierStandard, FactTierHost:
		default:
			return fmt.Errorf("fact %q has no tier", f.name)
		}
		fns[f.name] = f.tier
	}
	for _, name := range requiredCoreFacts {
		tier, ok := facts[name]
		if !ok {
			return fmt.Errorf("core fact %q is not in FactCatalogue()", name)
		}
		if tier != FactTierCore {
			return fmt.Errorf("core fact %q is declared %q", name, tier)
		}
	}
	for _, name := range requiredCoreFunctions {
		tier, ok := fns[name]
		if !ok {
			return fmt.Errorf("core fact %q is not in FactCatalogue()", name)
		}
		if tier != FactTierCore {
			return fmt.Errorf("core fact %q is declared %q", name, tier)
		}
	}
	for profile, members := range standardProfileFacts {
		for _, name := range members {
			tier, ok := facts[name]
			if !ok {
				if _, isFn := fns[name]; isFn {
					continue
				}
				return fmt.Errorf("standard profile %q member %q is not in FactCatalogue()", profile, name)
			}
			if tier != FactTierStandard {
				return fmt.Errorf("standard profile %q member %q is declared %q", profile, name, tier)
			}
		}
	}
	return nil
}

// StandardProfiles returns the profile names in stable order.
func StandardProfiles() []string {
	out := make([]string, 0, len(standardProfileFacts))
	for name := range standardProfileFacts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// StandardProfileMembers returns a profile's frozen membership.
func StandardProfileMembers(profile string) []string {
	return append([]string(nil), standardProfileFacts[profile]...)
}
