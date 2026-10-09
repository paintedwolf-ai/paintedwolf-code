package oar

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

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

		out["fire_count"], out["breaker_count"], out["anchor"] = gc.Counters.FireCount, gc.Counters.BreakerCount, gc.Anchor
	}
	return out
}

func occurrenceToolContract(gc *GuardContext) toolcontract.Contract {
	if gc == nil {
		return toolcontract.Contract{}
	}
	contract, _ := toolcontract.Lookup(gc.Invocation.Tool)
	return contract
}

func bareActivation(gc *GuardContext) map[string]any {
	if gc == nil {
		gc = NewGuardContext()
	}
	out := map[string]any{
		"tool":                            gc.Invocation.Tool,
		"tool_args":                       mapOrEmpty(gc.Invocation.ToolArgs),
		"last_assistant":                  gc.Session.LastAssistant,
		"turn_tools":                      strList(gc.Session.TurnTools),
		"session_posture":                 gc.Session.SessionPosture,
		"surface":                         gc.Session.Surface,
		"profile":                         gc.Session.Profile,
		"workers_idle":                    gc.Workers.WorkersIdle,
		"worker_spawn_blocked":            gc.Workers.WorkerSpawnBlocked,
		"active_worker_count":             gc.Workers.ActiveWorkerCount,
		"pending_overlay_promote":         gc.Workers.PendingOverlayPromote,
		"overlay_state":                   gc.Workers.OverlayState,
		"surface_may_finish":              gc.Session.SurfaceMayFinish,
		"is_host_cycle_turn":              gc.Session.IsHostCycleTurn,
		"batch_phase":                     gc.Workflow.BatchPhase,
		"batch_closed":                    gc.Workflow.BatchClosed,
		"progress_open_items":             gc.Progress.ProgressOpenItems,
		"progress_has_open_steps":         gc.Progress.ProgressHasOpenSteps,
		"has_completion_report":           gc.Progress.HasCompletionReport,
		"task_envelope_echo":              gc.Progress.TaskEnvelopeEcho,
		"closeout_surface":                gc.Session.CloseoutSurface,
		"progress_reconcile_needed":       gc.Progress.ProgressReconcileNeeded,
		"verify_required":                 gc.Progress.VerifyRequired,
		"verifier_pass":                   gc.Progress.VerifierPass,
		"synthesis_wrapup_tool_forbidden": gc.Workflow.SynthesisWrapupToolForbidden,
		"progress_closure_armed":          gc.Progress.ProgressClosureArmed,
		"progress_gated_tool":             gc.Progress.ProgressGatedTool,
		"progress_missing":                gc.Progress.ProgressMissing,
		"path_is_worker_branch":           gc.Workers.PathIsWorkerBranch,
		"verify_has_command":              gc.Progress.VerifyHasCommand,
		"verify_declared":                 gc.Progress.VerifyDeclared,
		"progress_closed_beyond_baseline": gc.Progress.ProgressClosedBeyondBaseline,
		"progress_reconciled_since_arm":   gc.Progress.ProgressReconciledSinceArm,
		"pending_user_input":              gc.Session.PendingUserInput,
		"stub_valid":                      gc.Session.StubValid,
		"tool_allowed_for_profile":        gc.Invocation.ToolAllowedForProfile,
		"habit_redirect_match":            gc.Invocation.HabitRedirectMatch,
		"write_roots":                     strList(gc.Access.WriteRoots),
		"pattern_parse_ok":                gc.Invocation.PatternParseOK,
		"arg_validation_errors":           strList(gc.Invocation.ArgValidationErrors),
		"worker_leg":                      gc.Session.WorkerLeg,
		"capability_request_fields":       occurrenceToolContract(gc).CapabilityRequestFields(),
		"supports_local_listen":           occurrenceToolContract(gc).Supports(toolcontract.CapabilityLocalListen),
		"supports_loopback_connect":       occurrenceToolContract(gc).Supports(toolcontract.CapabilityLoopbackConnect),
		"action_host_resources":           strList(gc.Access.ActionHostResources),
		"action_host_resource_denials":    strList(gc.Access.ActionHostResourceDenials),
		"confine_applied":                 gc.Execution.ConfineApplied,
		"network_mode":                    gc.Execution.NetworkMode,
		"denial_subject":                  gc.Execution.DenialSubject,
		"confine_signals":                 strList(gc.Execution.ConfineSignals),
		"failed_stages":                   strList(gc.Execution.FailedStages),
		"process_running":                 gc.Execution.ProcessRunning,
		"sandbox_refusals":                strList(gc.Execution.SandboxRefusals),
		"refused_write_paths":             strList(gc.Refusals.RefusedWritePaths),
		"refused_write_grants":            strList(gc.Refusals.RefusedWriteGrants),
		"refused_read_paths":              strList(gc.Refusals.RefusedReadPaths),
		"refused_read_grants":             strList(gc.Refusals.RefusedReadGrants),
		"receiving_tool_summarize":        gc.Invocation.Tool == "summarize",
		"refused_terminal_read_paths":     strList(gc.Refusals.RefusedTerminalReadPaths),
		"refused_terminal_write_paths":    strList(gc.Refusals.RefusedTerminalWritePaths),
		"sandbox_refusal_witness":         gc.Refusals.SandboxRefusalWitness,
		"refused_socket_paths":            strList(gc.Refusals.RefusedSocketPaths),
		"refused_connect_ports":           strList(gc.Refusals.RefusedConnectPorts),
		"refused_listen_ports":            strList(gc.Refusals.RefusedListenPorts),
		"refused_signals":                 strList(gc.Refusals.RefusedSignals),
		"unsandboxed_refusals":            strList(gc.Refusals.UnsandboxedRefusals),
		"worktree_stale_paths":            strList(gc.Source.WorktreeStalePaths),
		"worktree_leftover_paths":         strList(gc.Source.WorktreeLeftoverPaths),
		"worktree_conflict_paths":         strList(gc.Source.WorktreeConflictPaths),
		"mode_bits":                       gc.Execution.ModeBits,
		"tool_args_fingerprint":           gc.Invocation.ToolArgsFingerprint,
		"posture_unresolved":              gc.Session.PostureUnresolved,
		"tool_is_state":                   gc.Invocation.ToolIsState,
		"tool_is_delegation":              gc.Invocation.ToolIsDelegation,
		"tool_is_task":                    gc.Invocation.ToolIsTask,
		"tool_payload_chunkable":          gc.Invocation.ToolPayloadChunkable,
		"tool_is_handoff":                 gc.Invocation.ToolIsHandoff,
		"pack_runner_task":                gc.Invocation.PackRunnerTask,
		"agent_is_plan_writer":            gc.Workers.AgentIsPlanWriter,
		"disallowed_agent":                gc.Workers.DisallowedAgent,
		"unobserved_cited_paths":          strList(gc.Grounding.UnobservedCitedPaths),
		"unobserved_cited_urls":           strList(gc.Grounding.UnobservedCitedURLs),
		"unobserved_cited_handles":        strList(gc.Grounding.UnobservedCitedHandles),
		"citation_fields_present":         gc.Grounding.CitationFieldsPresent,
		"claims_completion":               gc.Grounding.ClaimsCompletion,
		"has_matching_ledger_job":         gc.Grounding.HasMatchingLedgerJob,
		"ledger_criteria_met":             gc.Grounding.LedgerCriteriaMet,
		"worker_summary_present":          gc.Grounding.WorkerSummaryPresent,
		"worker_artifact_present":         gc.Grounding.WorkerArtifactPresent,
		"worker_artifact_measured":        gc.Grounding.WorkerArtifactMeasured,
		"files_touched":                   strList(gc.Grounding.FilesTouched),
		"summary_length":                  gc.Grounding.SummaryLength,
		"repeat_count":                    gc.Counters.RepeatCount,
		"fruitless_search_run":            gc.Counters.FruitlessSearchRun,
		"breaker_count":                   gc.Counters.BreakerCount,
		"same_code_reject_run":            gc.Counters.SameCodeRejectRun,
		"code_reject_responses":           gc.Counters.CodeRejectResponses,
		"deferred_unactivated":            gc.Counters.DeferredUnactivated,
		"worker_attempted_mutation":       gc.Workers.WorkerAttemptedMutation,
		"batch_ready_ignoring_progress":   gc.Workflow.BatchReadyIgnoringProgress,
		"synthesis_delay_count":           gc.Workflow.SynthesisDelayCount,
		"scope_mode":                      gc.Workers.ScopeMode,
		"profile_mutation_capable":        gc.Workers.ProfileMutationCapable,
		"base_overlay_id":                 gc.Workers.BaseOverlayID,
		"base_overlay_resolves":           gc.Workers.BaseOverlayResolves,
		"base_overlay_checked":            gc.Workers.BaseOverlayChecked,
		"base_overlay_pending":            gc.Workers.BaseOverlayPending,
		"active_read_count":               gc.Workers.ActiveReadCount,
		"active_write_count":              gc.Workers.ActiveWriteCount,
		"max_workers":                     gc.Workers.MaxWorkers,
		"max_read_workers":                gc.Workers.MaxReadWorkers,
		"max_write_workers":               gc.Workers.MaxWriteWorkers,
		"citation_unverifiable":           gc.Grounding.CitationUnverifiable,
		"scout_survey_evidence_present":   gc.Grounding.ScoutSurveyEvidencePresent,
		"surface_claim_ungrounded":        gc.Grounding.SurfaceClaimUngrounded,
		"page_measure_ungrounded":         gc.Grounding.PageMeasureUngrounded,
		"agent_is_scout":                  gc.Workers.AgentIsScout,
		"agent_is_implementer":            gc.Workers.AgentIsImplementer,
		"profile_surveys_project_tree":    gc.Grounding.ProfileSurveysProjectTree,
		"profile_fetches_urls":            gc.Grounding.ProfileFetchesURLs,
		"repo_known_empty":                gc.Grounding.RepoKnownEmpty,
		"last_audit_ungrounded":           gc.Grounding.LastAuditUngrounded,
		"grounding_escalated":             gc.Grounding.GroundingEscalated,
		"prompt_injection_score":          gc.Content.PromptInjectionScore,
		"jailbreak_score":                 gc.Content.JailbreakScore,
		"pii_entities":                    mapList(gc.Content.PIIEntities),
		"secret_matches":                  mapList(gc.Content.SecretMatches),
		"recent_tool_names":               strList(gc.Session.RecentToolNames),
		"mcp_provider_id":                 gc.MCP.MCPProviderID,
		"mcp_tool_name":                   gc.MCP.MCPToolName,
		"mcp_qualified_tool":              gc.MCP.MCPQualifiedTool,
		"mcp_provider_configured":         gc.MCP.MCPProviderConfigured,
		"mcp_provider_enabled":            gc.MCP.MCPProviderEnabled,
		"mcp_call_ok":                     gc.MCP.MCPCallOK,
		"mcp_error_code":                  gc.MCP.MCPErrorCode,
		"mcp_schema_matched":              gc.MCP.MCPSchemaMatched,
		"editorconfig_mismatch":           gc.Source.EditorConfigMismatch,
		"syntax_check_overridden":         gc.Source.SyntaxCheckOverridden,
		"source_analysis_unavailable":     gc.Source.SourceAnalysisUnavailable,
		"http_request_web_page":           gc.Invocation.HTTPRequestWebPage,
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
		"command_not_argv":   gc.Invocation.CommandNotArgv,
		"is_directory":       gc.Rejection.IsDirectory,
		"not_found":          gc.Rejection.NotFound,
		"path_denied":        gc.Rejection.PathDenied,
		"bulk_denied":        gc.Rejection.BulkDenied,
		"binary_denied":      gc.Rejection.BinaryDenied,
		"mode_denied":        gc.Rejection.ModeDenied,
		"path_escape":        gc.Rejection.PathEscape,
		"beyond_eof":         gc.Rejection.BeyondEOF,
		"not_running":        gc.Rejection.NotRunning,
		"unsupported":        gc.Rejection.Unsupported,
		"resource_limit":     gc.Rejection.ResourceLimit,
		"conflict":           gc.Rejection.Conflict,
		"path_required":      gc.Rejection.PathRequired,
		"id_required":        gc.Rejection.IDRequired,
		"policy_denied":      gc.Rejection.PolicyDenied,
		"unknown_target":     gc.Rejection.UnknownTarget,
		"missing":            gc.Rejection.Missing,
		"forbidden":          gc.Rejection.Forbidden,
		"selector_empty":     gc.Rejection.SelectorEmpty,
		"selector_ambiguous": gc.Rejection.SelectorAmbiguous,
		"reject_observation": gc.Rejection.RejectObservation,
		"rejection_code":     gc.ObservedRejectCode,
	}
}

// workflowActivation defines manifest-phase and durable workflow gate observations.
func workflowActivation(gc *GuardContext) map[string]any {
	return map[string]any{
		"phase":                         gc.Session.Phase,
		"review_loop_active":            gc.Workflow.ReviewLoopActive,
		"plan_awaiting_approval":        gc.Workflow.PlanAwaitingApproval,
		"review_verdict_gate_open":      gc.Workflow.ReviewVerdictGateOpen,
		"verdict_delay_count":           gc.Workflow.VerdictDelayCount,
		"closeout_gates_open":           gc.Workflow.CloseoutGatesOpen,
		"closeout_gate_open_leaves":     gc.Workflow.CloseoutGateOpenLeaves,
		"closeout_gate_delay_count":     gc.Workflow.CloseoutGateDelayCount,
		"workflow_report_phase_pending": gc.Workflow.WorkflowReportPhasePending,
		"phase_obligation_pending":      gc.Workflow.PhaseObligationPending,
		"phase_obligation_kinds":        gc.Workflow.PhaseObligationKinds,
	}
}

// standardActivation defines core and standard-profile observations, including
// the portable tool-profile fields copy may bind.
func standardActivation(gc *GuardContext) map[string]any {
	return map[string]any{
		"anchor":                     gc.Anchor,
		"fire_count":                 gc.Counters.FireCount,
		"permission_profile":         gc.Session.PermissionProfile,
		"principal":                  gc.Session.Principal,
		"principal_roles":            strList(gc.Session.PrincipalRoles),
		"content_length":             gc.Content.ContentLength,
		"content_roles":              strList(gc.Content.ContentRoles),
		"content_origins":            strList(gc.Content.ContentOrigins),
		"content_authorities":        strList(gc.Content.ContentAuthorities),
		"content_trust_tiers":        strList(gc.Content.ContentTrustTiers),
		"content_sources":            strList(gc.Content.ContentSources),
		"content_segment_count":      gc.Content.ContentSegmentCount,
		"content_contains_untrusted": gc.Content.ContentContainsUntrusted,
		"arg_validation_reason":      gc.Invocation.ArgValidationReason,
		"arg_validation_field":       gc.Invocation.ArgValidationField,
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
