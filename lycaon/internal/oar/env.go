package oar

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SupportedSpecVersion is the only accepted major.minor for a rule's oar marker.
const SupportedSpecVersion = "1.0"

// evalHolder holds runtime state for one anchor occurrence.
type evalHolder struct {
	gc          *GuardContext
	counters    *CounterStore
	rules       *RuleSet
	currentRule *Rule
	// snapshot precedes this occurrence's side-effects ([OAR-FIRE-6]).
	snapshot map[string]map[CounterKind]int64
}

func newEvalHolder(gc *GuardContext, rules *RuleSet, counters *CounterStore) *evalHolder {
	h := &evalHolder{}
	h.gc = gc
	h.rules = rules
	h.counters = counters
	if gc != nil && counters != nil {
		h.beginOccurrence(counters, gc.SessionID)
	}
	return h
}

func (h *evalHolder) set(gc *GuardContext) { h.gc = gc }
func (h *evalHolder) get() *GuardContext   { return h.gc }

func (h *evalHolder) setCurrentRule(r *Rule) {
	if h != nil {
		h.currentRule = r
	}
}

// counterOf reads kind for ruleID in the evaluating session. An absent store or
// session yields 0, the same value an unfired rule has ([OAR-FIRE-11]).
func (h *evalHolder) counterOf(ruleID string, kind CounterKind) int64 {
	if h == nil {
		return 0
	}
	gc := h.get()
	store := h.counters
	if gc == nil || store == nil || gc.SessionID == "" {
		return 0
	}
	ns := ""
	if current := h.currentRule; current != nil {
		ns = current.Namespace
	}
	target := resolveCounterTarget(h.rules, ruleID, ns)
	key := ruleID
	if target != nil {
		key = counterKeyFor(target, gc)
	}
	return h.getCounter(gc.SessionID, key, kind)
}

func (h *evalHolder) beginOccurrence(store *CounterStore, sessionID string) {
	if h == nil {
		return
	}
	h.snapshot = store.CloneSession(sessionID)
}

func (h *evalHolder) getCounter(sessionID, key string, kind CounterKind) int64 {
	if h != nil && h.snapshot != nil {
		if m, ok := h.snapshot[key]; ok {
			return m[kind]
		}
		return 0
	}
	if h == nil {
		return 0
	}
	store := h.counters
	if store == nil {
		return 0
	}
	return store.Get(sessionID, key, kind)
}

func resolveCounterTarget(rs *RuleSet, ref, namespace string) *Rule {
	if rs == nil {
		return nil
	}
	qualified := ref
	if !strings.Contains(ref, "/") && namespace != "" {
		qualified = namespace + "/" + ref
	}
	for _, r := range rs.All() {
		if r.Qualified() == qualified {
			return r
		}
	}
	return nil
}

func counterKeyFor(r *Rule, gc *GuardContext) string {
	if r == nil {
		return ""
	}
	q := r.Qualified()
	if strings.TrimSpace(r.CounterScope) == "" {
		return q
	}
	scope := scopedFactString(gc, r.CounterScope)
	return q + "#" + scope
}

// scopedFactString returns the string value a rule observes for the named fact.
func scopedFactString(gc *GuardContext, name string) string {
	value, _ := activation(gc)[name].(string)
	return value
}

func (h *evalHolder) counterScope(target *Rule) (string, error) {
	if target == nil {
		return "", fmt.Errorf("[OAR-FIRE-11] unknown counter target")
	}
	if target.CounterScope == "" {
		return target.Qualified(), nil
	}
	if err := h.gc.Ensure(target.CounterScope); err != nil {
		return "", err
	}
	facts := activation(h.gc)
	value := facts[target.CounterScope]
	if target.document != nil {
		var err error
		value, err = target.document.Fact(target.CounterScope, facts)
		if err != nil {
			return "", err
		}
	}
	if value == nil {
		value = ""
	}
	scope, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("[OAR-FACT-26] %s must be a string", target.CounterScope)
	}
	return target.Qualified() + "#" + scope, nil
}

func (h *evalHolder) readCounter(ref string, kind CounterKind) (any, error) {
	namespace := ""
	if h.currentRule != nil {
		namespace = h.currentRule.Namespace
	}
	target := resolveCounterTarget(h.rules, ref, namespace)
	key, err := h.counterScope(target)
	if err != nil {
		return nil, err
	}
	return h.getCounter(h.gc.SessionID, key, kind), nil
}

func specObservationFuncs(holder *evalHolder) map[string]func(any) (any, error) {
	out := make(map[string]func(any) (any, error), len(observationFns))
	for _, f := range observationFns {
		fn := f
		name := publishedName(fn.name, fn.tier)
		out[name] = func(arg any) (any, error) {
			if gc := holder.get(); gc != nil && gc.Published != nil {
				if v, ok := gc.Published[name]; ok {
					return v, nil
				}
			}
			s, ok := arg.(string)
			if !ok {
				return zeroForFactType(fn.ret), nil
			}
			if name == "fire_count_of" {
				return holder.readCounter(s, CounterFire)
			}
			if name == "breaker_count_of" {
				return holder.readCounter(s, CounterBreaker)
			}
			return fn.bind(holder, s), nil
		}
	}
	return out
}

func publishedName(name string, tier FactTier) string {
	if tier == FactTierHost {
		return oarcopy.HostFactNamespace + "." + name
	}
	return name
}

func zeroForFactType(t oarcore.FactType) any {
	switch t {
	case oarcore.TypeString:
		return ""
	case oarcore.TypeInt:
		return int64(0)
	default:
		return false
	}
}

// activation publishes occurrence facts under their rule-visible names.
func activation(gc *GuardContext) map[string]any {
	bare := bareActivation(gc)
	tiers := factTiers()
	out := make(map[string]any, len(bare))
	for name, value := range bare {
		out[publishedName(name, tiers[name])] = value
	}
	if gc != nil {
		for name, value := range oarcopy.FactsFromData(gc.ObservationData) {
			// Rejection presentation metadata cannot replace canonical protocol observations.
			if !strings.HasPrefix(name, "paintedwolf.") || name == "paintedwolf.rejection_code" {
				continue
			}
			// Host presentation fields are declared strings; serialize their structured source at the producer boundary.
			if declaredFactTypes[name] == "string" && strings.HasPrefix(name, "paintedwolf.") {
				if value == nil {
					value = ""
				} else if _, ok := value.(string); !ok {
					if encoded, err := json.Marshal(value); err == nil {
						value = string(encoded)
					}
				}
			}
			out[name] = value
		}
		for name, value := range gc.Published {
			out[name] = value
		}

		out["fire_count"], out["breaker_count"], out["anchor"] = gc.FireCount, gc.BreakerCount, gc.Anchor
	}
	return out
}

func occurrenceToolContract(gc *GuardContext) toolcontract.Contract {
	if gc == nil {
		return toolcontract.Contract{}
	}
	contract, _ := toolcontract.Lookup(gc.Tool)
	return contract
}

func bareActivation(gc *GuardContext) map[string]any {
	if gc == nil {
		gc = NewGuardContext()
	}
	out := map[string]any{
		"tool":                            gc.Tool,
		"tool_args":                       mapOrEmpty(gc.ToolArgs),
		"last_assistant":                  gc.LastAssistant,
		"turn_tools":                      strList(gc.TurnTools),
		"session_posture":                 gc.SessionPosture,
		"surface":                         gc.Surface,
		"profile":                         gc.Profile,
		"workers_idle":                    gc.WorkersIdle,
		"worker_spawn_blocked":            gc.WorkerSpawnBlocked,
		"active_worker_count":             gc.ActiveWorkerCount,
		"pending_overlay_promote":         gc.PendingOverlayPromote,
		"overlay_state":                   gc.OverlayState,
		"surface_may_finish":              gc.SurfaceMayFinish,
		"is_host_cycle_turn":              gc.IsHostCycleTurn,
		"batch_phase":                     gc.BatchPhase,
		"batch_closed":                    gc.BatchClosed,
		"progress_open_items":             gc.ProgressOpenItems,
		"progress_has_open_steps":         gc.ProgressHasOpenSteps,
		"has_completion_report":           gc.HasCompletionReport,
		"task_envelope_echo":              gc.TaskEnvelopeEcho,
		"closeout_surface":                gc.CloseoutSurface,
		"progress_reconcile_needed":       gc.ProgressReconcileNeeded,
		"verify_required":                 gc.VerifyRequired,
		"verifier_pass":                   gc.VerifierPass,
		"synthesis_wrapup_tool_forbidden": gc.SynthesisWrapupToolForbidden,
		"progress_closure_armed":          gc.ProgressClosureArmed,
		"progress_gated_tool":             gc.ProgressGatedTool,
		"progress_missing":                gc.ProgressMissing,
		"path_is_worker_branch":           gc.PathIsWorkerBranch,
		"verify_has_command":              gc.VerifyHasCommand,
		"verify_declared":                 gc.VerifyDeclared,
		"progress_closed_beyond_baseline": gc.ProgressClosedBeyondBaseline,
		"progress_reconciled_since_arm":   gc.ProgressReconciledSinceArm,
		"pending_user_input":              gc.PendingUserInput,
		"stub_valid":                      gc.StubValid,
		"tool_allowed_for_profile":        gc.ToolAllowedForProfile,
		"habit_redirect_match":            gc.HabitRedirectMatch,
		"write_roots":                     strList(gc.WriteRoots),
		"pattern_parse_ok":                gc.PatternParseOK,
		"arg_validation_errors":           strList(gc.ArgValidationErrors),
		"worker_leg":                      gc.WorkerLeg,
		"capability_request_fields":       occurrenceToolContract(gc).CapabilityRequestFields(),
		"supports_local_listen":           occurrenceToolContract(gc).Supports(toolcontract.CapabilityLocalListen),
		"supports_loopback_connect":       occurrenceToolContract(gc).Supports(toolcontract.CapabilityLoopbackConnect),
		"action_host_resources":           strList(gc.ActionHostResources),
		"action_host_resource_denials":    strList(gc.ActionHostResourceDenials),
		"confine_applied":                 gc.ConfineApplied,
		"network_mode":                    gc.NetworkMode,
		"denial_subject":                  gc.DenialSubject,
		"confine_signals":                 strList(gc.ConfineSignals),
		"failed_stages":                   strList(gc.FailedStages),
		"process_running":                 gc.ProcessRunning,
		"sandbox_refusals":                strList(gc.SandboxRefusals),
		"refused_write_paths":             strList(gc.RefusedWritePaths),
		"refused_write_grants":            strList(gc.RefusedWriteGrants),
		"refused_read_paths":              strList(gc.RefusedReadPaths),
		"refused_read_grants":             strList(gc.RefusedReadGrants),
		"refused_socket_paths":            strList(gc.RefusedSocketPaths),
		"refused_connect_ports":           strList(gc.RefusedConnectPorts),
		"refused_listen_ports":            strList(gc.RefusedListenPorts),
		"refused_signals":                 strList(gc.RefusedSignals),
		"unsandboxed_refusals":            strList(gc.UnsandboxedRefusals),
		"worktree_stale_paths":            strList(gc.WorktreeStalePaths),
		"worktree_leftover_paths":         strList(gc.WorktreeLeftoverPaths),
		"worktree_conflict_paths":         strList(gc.WorktreeConflictPaths),
		"mode_bits":                       gc.ModeBits,
		"tool_args_fingerprint":           gc.ToolArgsFingerprint,
		"posture_unresolved":              gc.PostureUnresolved,
		"high_risk_tool":                  gc.HighRiskTool,
		"tool_is_state":                   gc.ToolIsState,
		"tool_is_delegation":              gc.ToolIsDelegation,
		"tool_is_task":                    gc.ToolIsTask,
		"tool_payload_chunkable":          gc.ToolPayloadChunkable,
		"tool_is_handoff":                 gc.ToolIsHandoff,
		"pack_runner_task":                gc.PackRunnerTask,
		"agent_is_plan_writer":            gc.AgentIsPlanWriter,
		"disallowed_agent":                gc.DisallowedAgent,
		"unobserved_cited_paths":          strList(gc.UnobservedCitedPaths),
		"unobserved_cited_urls":           strList(gc.UnobservedCitedURLs),
		"unobserved_cited_handles":        strList(gc.UnobservedCitedHandles),
		"citation_fields_present":         gc.CitationFieldsPresent,
		"claims_completion":               gc.ClaimsCompletion,
		"has_matching_ledger_job":         gc.HasMatchingLedgerJob,
		"ledger_criteria_met":             gc.LedgerCriteriaMet,
		"worker_summary_present":          gc.WorkerSummaryPresent,
		"worker_artifact_present":         gc.WorkerArtifactPresent,
		"worker_artifact_measured":        gc.WorkerArtifactMeasured,
		"files_touched":                   strList(gc.FilesTouched),
		"summary_length":                  gc.SummaryLength,
		"repeat_count":                    gc.RepeatCount,
		"fruitless_search_run":            gc.FruitlessSearchRun,
		"breaker_count":                   gc.BreakerCount,
		"same_code_reject_run":            gc.SameCodeRejectRun,
		"code_reject_responses":           gc.CodeRejectResponses,
		"deferred_unactivated":            gc.DeferredUnactivated,
		"worker_attempted_mutation":       gc.WorkerAttemptedMutation,
		"batch_ready_ignoring_progress":   gc.BatchReadyIgnoringProgress,
		"synthesis_delay_count":           gc.SynthesisDelayCount,
		"scope_mode":                      gc.ScopeMode,
		"profile_mutation_capable":        gc.ProfileMutationCapable,
		"base_overlay_id":                 gc.BaseOverlayID,
		"base_overlay_resolves":           gc.BaseOverlayResolves,
		"base_overlay_checked":            gc.BaseOverlayChecked,
		"base_overlay_pending":            gc.BaseOverlayPending,
		"active_read_count":               gc.ActiveReadCount,
		"active_write_count":              gc.ActiveWriteCount,
		"max_workers":                     gc.MaxWorkers,
		"max_read_workers":                gc.MaxReadWorkers,
		"max_write_workers":               gc.MaxWriteWorkers,
		"citation_unverifiable":           gc.CitationUnverifiable,
		"scout_survey_evidence_present":   gc.ScoutSurveyEvidencePresent,
		"surface_claim_ungrounded":        gc.SurfaceClaimUngrounded,
		"page_measure_ungrounded":         gc.PageMeasureUngrounded,
		"agent_is_scout":                  gc.AgentIsScout,
		"agent_is_implementer":            gc.AgentIsImplementer,
		"profile_surveys_project_tree":    gc.ProfileSurveysProjectTree,
		"profile_fetches_urls":            gc.ProfileFetchesURLs,
		"repo_known_empty":                gc.RepoKnownEmpty,
		"last_audit_ungrounded":           gc.LastAuditUngrounded,
		"grounding_escalated":             gc.GroundingEscalated,
		"prompt_injection_score":          gc.PromptInjectionScore,
		"jailbreak_score":                 gc.JailbreakScore,
		"pii_entities":                    mapList(gc.PIIEntities),
		"secret_matches":                  mapList(gc.SecretMatches),
		"recent_tool_names":               strList(gc.RecentToolNames),
		"mcp_provider_id":                 gc.MCPProviderID,
		"mcp_tool_name":                   gc.MCPToolName,
		"mcp_qualified_tool":              gc.MCPQualifiedTool,
		"mcp_provider_configured":         gc.MCPProviderConfigured,
		"mcp_provider_enabled":            gc.MCPProviderEnabled,
		"mcp_call_ok":                     gc.MCPCallOK,
		"mcp_error_code":                  gc.MCPErrorCode,
		"mcp_schema_matched":              gc.MCPSchemaMatched,
		"editorconfig_mismatch":           gc.EditorConfigMismatch,
		"syntax_check_overridden":         gc.SyntaxCheckOverridden,
		"source_analysis_unavailable":     gc.SourceAnalysisUnavailable,
		"http_request_web_page":           gc.HTTPRequestWebPage,
	}
	for name, value := range rejectObservationActivation(gc) {
		out[name] = value
	}
	for name, value := range standardActivation(gc) {
		out[name] = value
	}
	for name, value := range workflowActivation(gc) {
		out[name] = value
	}
	return out
}

// rejectObservationActivation defines the structured tool-rejection vocabulary:
// what the host observed about a call it refused, one fact per cause.
func rejectObservationActivation(gc *GuardContext) map[string]any {
	return map[string]any{
		"command_not_argv":   gc.CommandNotArgv,
		"is_directory":       gc.IsDirectory,
		"not_found":          gc.NotFound,
		"path_denied":        gc.PathDenied,
		"bulk_denied":        gc.BulkDenied,
		"binary_denied":      gc.BinaryDenied,
		"mode_denied":        gc.ModeDenied,
		"path_escape":        gc.PathEscape,
		"beyond_eof":         gc.BeyondEOF,
		"not_running":        gc.NotRunning,
		"unsupported":        gc.Unsupported,
		"resource_limit":     gc.ResourceLimit,
		"conflict":           gc.Conflict,
		"path_required":      gc.PathRequired,
		"id_required":        gc.IDRequired,
		"policy_denied":      gc.PolicyDenied,
		"unknown_target":     gc.UnknownTarget,
		"missing":            gc.Missing,
		"forbidden":          gc.Forbidden,
		"selector_empty":     gc.SelectorEmpty,
		"selector_ambiguous": gc.SelectorAmbiguous,
		"reject_observation": gc.RejectObservation,
		"rejection_code":     gc.ObservedRejectCode,
	}
}

// workflowActivation defines manifest-phase and durable workflow gate observations.
func workflowActivation(gc *GuardContext) map[string]any {
	return map[string]any{
		"phase":                         gc.Phase,
		"review_loop_active":            gc.ReviewLoopActive,
		"plan_awaiting_approval":        gc.PlanAwaitingApproval,
		"review_verdict_gate_open":      gc.ReviewVerdictGateOpen,
		"verdict_delay_count":           gc.VerdictDelayCount,
		"closeout_gates_open":           gc.CloseoutGatesOpen,
		"closeout_gate_open_leaves":     gc.CloseoutGateOpenLeaves,
		"closeout_gate_delay_count":     gc.CloseoutGateDelayCount,
		"workflow_report_phase_pending": gc.WorkflowReportPhasePending,
		"phase_obligation_pending":      gc.PhaseObligationPending,
		"phase_obligation_kinds":        gc.PhaseObligationKinds,
	}
}

// standardActivation defines core and standard-profile observations, including
// the portable tool-profile fields copy may bind.
func standardActivation(gc *GuardContext) map[string]any {
	return map[string]any{
		"anchor":                     gc.Anchor,
		"fire_count":                 gc.FireCount,
		"permission_profile":         gc.PermissionProfile,
		"principal":                  gc.Principal,
		"principal_roles":            strList(gc.PrincipalRoles),
		"content_length":             gc.ContentLength,
		"content_roles":              strList(gc.ContentRoles),
		"content_origins":            strList(gc.ContentOrigins),
		"content_authorities":        strList(gc.ContentAuthorities),
		"content_trust_tiers":        strList(gc.ContentTrustTiers),
		"content_sources":            strList(gc.ContentSources),
		"content_segment_count":      gc.ContentSegmentCount,
		"content_contains_untrusted": gc.ContentContainsUntrusted,
		"arg_validation_reason":      gc.ArgValidationReason,
		"arg_validation_field":       gc.ArgValidationField,
	}
}

func mapOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func strList(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mapList(v []any) []map[string]any {
	if len(v) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(v))
	for _, item := range v {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// EvaluateCondition evaluates a standalone condition.
func EvaluateCondition(when string, gc *GuardContext) (bool, error) {
	return evalCondition(newEvalHolder(gc, nil, nil), when, gc)
}

func evalCondition(holder *evalHolder, when string, gc *GuardContext) (bool, error) {
	if strings.TrimSpace(when) == "" {
		return true, nil
	}
	holder.set(gc)
	env, err := capabilityEnvironment(InstalledCapabilityDocument())
	if err != nil {
		return false, err
	}
	return oarcore.EvaluateCondition(when, env, activation(gc), specObservationFuncs(holder))
}

func capabilityEnvironment(cap *CapabilityDocument) (*oarcore.Environment, error) {
	raw, err := json.Marshal(cap)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return oarcore.LoadCapability(document)
}

func specReachable(env *oarcore.Environment) map[string]bool {
	out := map[string]bool{}
	if env == nil {
		return out
	}
	for name := range env.Facts {
		out[name] = true
	}
	for name := range env.Functions {
		out[name] = true
	}
	return out
}

func checkWhenAgainstSpec(when string, cap *CapabilityDocument) error {
	if cap == nil {
		cap = InstalledCapabilityDocument()
	}
	if strings.TrimSpace(when) == "" {
		return nil
	}
	env, err := capabilityEnvironment(cap)
	if err != nil {
		return err
	}
	return oarcore.CheckCondition(when, env, specReachable(env))
}

func evalRuleWhen(holder *evalHolder, r *Rule, gc *GuardContext) (bool, error) {
	if r == nil {
		return true, nil
	}
	holder.set(gc)
	if r.document != nil {
		return r.document.EvaluateCondition(activation(gc), specObservationFuncs(holder))
	}
	return evalCondition(holder, r.When, gc)
}

// EvalPathOutsideScope is the observation for path_outside_scope(tool).
func EvalPathOutsideScope(gc *GuardContext, tool string) bool {
	if gc == nil {
		return false
	}
	if gc.PathOutsideScopeByTool != nil {
		if v, ok := gc.PathOutsideScopeByTool[tool]; ok {
			return v
		}
	}
	if tool == gc.Tool {
		return gc.PathOutsideScope
	}
	return false
}

// Typed argument accessors return zero for absent or mismatched types ([OAR-EXPR-19]).
func EvalToolArgString(gc *GuardContext, key string) string {
	if gc == nil {
		return ""
	}
	s, _ := gc.ToolArgs[key].(string)
	return s
}

func EvalToolArgInt(gc *GuardContext, key string) int64 {
	if gc == nil {
		return 0
	}
	switch v := gc.ToolArgs[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		// Decoded floats count as integers only when they have no fractional part.
		if v == float64(int64(v)) {
			return int64(v)
		}
	}
	return 0
}

func EvalToolArgBool(gc *GuardContext, key string) bool {
	if gc == nil {
		return false
	}
	b, _ := gc.ToolArgs[key].(bool)
	return b
}

// EvalSourceIncludes is the observation for source_includes(id).
func EvalSourceIncludes(gc *GuardContext, id string) bool {
	if gc == nil || gc.SourceIncludes == nil {
		return false
	}
	return gc.SourceIncludes[id]
}

// EvalHostResourceStatus and EvalHostResourcePolicy expose resource catalog state.
func EvalHostResourceStatus(gc *GuardContext, id string) string {
	if gc == nil || gc.HostResourceStatus == nil {
		return ""
	}
	if status, ok := gc.HostResourceStatus[id]; ok {
		return status
	}
	return ""
}

func EvalHostResourcePolicy(gc *GuardContext, id string) string {
	if gc == nil || gc.HostResourcePolicy == nil {
		return ""
	}
	if policy, ok := gc.HostResourcePolicy[id]; ok {
		return policy
	}
	return ""
}

// FlowMatches reports whether pattern is an ordered subsequence of recent tool names.
func FlowMatches(recent, pattern []string) bool {
	if len(pattern) == 0 {
		return true
	}
	i := 0
	for _, name := range recent {
		if name == pattern[i] {
			i++
			if i == len(pattern) {
				return true
			}
		}
	}
	return false
}
