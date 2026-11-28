package oar

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

var publishedBareNames = func() map[string]string {
	m := make(map[string]string, len(bareFactTiers))
	for name, tier := range bareFactTiers {
		m[name] = publishedName(name, tier)
	}
	return m
}()

func publishedKey(name string) string {
	if k, ok := publishedBareNames[name]; ok {
		return k
	}
	return publishedName(name, bareFactTiers[name])
}

// activation publishes occurrence facts under their rule-visible names.
// occurrenceIdentityFacts are host facts the occurrence itself establishes —
// rejection identity, caller, role, profile, posture, anchor, and counters —
// which occurrence data never replaces.
var occurrenceIdentityFacts = map[string]bool{
	"paintedwolf.rejection_code": true, "paintedwolf.principal": true, "paintedwolf.principal_roles": true,
	"paintedwolf.session_role": true, "paintedwolf.tool_profile": true, "paintedwolf.session_posture": true,
	"paintedwolf.anchor": true, "paintedwolf.credential_decisions": true,
}

func activation(gc *GuardContext) map[string]any {
	out := make(map[string]any, 200)
	fillBareActivation(out, gc)
	if gc != nil {
		for name, value := range oarcopy.FactsFromData(gc.ObservationData) {
			// Rejection presentation metadata cannot replace canonical protocol observations.
			if !strings.HasPrefix(name, "paintedwolf.") || occurrenceIdentityFacts[name] {
				continue
			}
			// Host presentation fields are declared strings; serialize structured values at the boundary.
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
		// The operation is the reached native operation beneath command, otherwise
		// the actual tool. Selectors name it without impersonating a tool call.
		operation, _ := out["paintedwolf.native_operation"].(string)
		if operation == "" {
			operation = gc.Invocation.Tool
		}
		out["paintedwolf.operation"] = operation
		// The canonical command spelling follows the observed native operation,
		// never diagnostic data; tool legs have no single command form.
		nativeOperation, _ := out["paintedwolf.native_operation"].(string)
		if form := oarcopy.CommandFormOf(nativeOperation); form != "" {
			out["paintedwolf.command_form"] = form
		} else {
			delete(out, "paintedwolf.command_form")
		}
		// Authoritative invocation and diagnostic facts overwrite any untrusted keys in ObservationData or Published.
		oarcopy.PublishInvocationRecovery(out, gc.Invocation.Tool, gc.Invocation.ToolArgs, gc.Invocation.CapabilityContract)
		narrowCapabilityRecoveryByProjectedSchema(out, gc.Reach, gc.Invocation.Tool)
		oarcopy.PublishDiagnosticKnowledge(out, gc.ObservationData)
		// A sandbox refusal names its sockets outside the observation data.
		if len(gc.Refusals.RefusedSocketPaths) > 0 {
			oarcopy.PublishSocketReferences(out, gc.Refusals.RefusedSocketPaths)
		}

		// Occurrence data never replaces the counts the engine maintains.
		out["fire_count"], out["breaker_count"], out["paintedwolf.anchor"] = gc.Counters.FireCount, gc.Counters.BreakerCount, gc.Anchor
		out["same_code_reject_run"], out["same_call_run"] = gc.Counters.SameCodeRejectRun, gc.Counters.SameCallRun
		out["paintedwolf.credential_decisions"] = gc.Counters.CredentialDecisions
	}
	return out
}

func narrowCapabilityRecoveryByProjectedSchema(out map[string]any, reach toolreach.Request, tool string) {
	if !reach.Known() || tool == "" || reach.Status(tool) != toolreach.Callable {
		return
	}
	if _, hasSchema := reach.ToolSchema(tool); !hasSchema {
		return
	}
	if cur, ok := out["paintedwolf.capability_request_fields"].([]string); ok {
		admitted := make([]string, 0, len(cur))
		for _, field := range cur {
			if reach.AcceptsField(tool, "capability_request."+field) {
				admitted = append(admitted, field)
			}
		}
		out["paintedwolf.capability_request_fields"] = admitted
	}
	for _, field := range toolcontract.CapabilityRequestFields() {
		if !reach.AcceptsField(tool, "capability_request."+field) {
			out["paintedwolf.supports_"+field] = false
			out["paintedwolf.missing_capability_"+field] = false
		}
	}
}

func fillBareActivation(out map[string]any, gc *GuardContext) {
	if gc == nil {
		gc = NewGuardContext()
	}
	set := func(name string, val any) {
		out[publishedKey(name)] = val
	}
	set("tool", gc.Invocation.Tool)
	set("tool_args", mapOrEmpty(gc.Invocation.ToolArgs))
	set("command_operation_reachable", false)
	set("command_external_reachable", false)
	set("admitted_process_capability_fields", []string{})
	set("command_loadable_without_schema", false)
	set("admitted_command_forms", []string{})
	set("unpublished_tool_text_pending", gc.Session.UnpublishedToolTextPending)
	set("mid_turn_citations_required", gc.Session.MidTurnCitationsRequired)
	set("mid_turn_citation_attempted", gc.Session.MidTurnCitationAttempted)
	set("mid_turn_evidence_unreadable", gc.Session.MidTurnEvidenceUnreadable)
	set("last_assistant", gc.Session.LastAssistant)
	set("turn_tools", strList(gc.Session.TurnTools))
	set("session_posture", gc.Session.SessionPosture)
	set("session_mode", gc.Session.SessionMode)
	set("ask_worker_writes", gc.Workers.AskWorkerWrites)
	set("surface", gc.Session.Surface)
	set("session_role", gc.Session.Role)
	set("workers_idle", gc.Workers.WorkersIdle)
	set("worker_spawn_blocked", gc.Workers.WorkerSpawnBlocked)
	set("active_worker_count", gc.Workers.ActiveWorkerCount)
	set("pending_overlay_promote", gc.Workers.PendingOverlayPromote)
	set("surface_may_finish", gc.Session.SurfaceMayFinish)
	set("is_host_cycle_turn", gc.Session.IsHostCycleTurn)
	set("batch_phase", gc.Workflow.BatchPhase)
	set("batch_closed", gc.Workflow.BatchClosed)
	set("progress_open_items", gc.Progress.ProgressOpenItems)
	set("progress_has_one_open_item", gc.Progress.ProgressOpenItems == 1)
	set("progress_has_open_steps", gc.Progress.ProgressHasOpenSteps)
	set("closeout_has_answer", gc.Progress.CloseoutHasAnswer)
	set("closeout_only_repair_directives", gc.Progress.CloseoutOnlyRepairDirectives)
	set("task_envelope_echo", gc.Progress.TaskEnvelopeEcho)
	set("closeout_surface", gc.Session.CloseoutSurface)
	set("progress_reconcile_needed", gc.Progress.ProgressReconcileNeeded)
	set("verify_required", gc.Progress.VerifyRequired)
	set("verifier_pass", gc.Progress.VerifierPass)
	set("synthesis_wrapup_tool_forbidden", gc.Workflow.SynthesisWrapupToolForbidden)
	set("progress_closure_armed", gc.Progress.ProgressClosureArmed)
	set("progress_gated_tool", gc.Progress.ProgressGatedTool)
	set("progress_missing", gc.Progress.ProgressMissing)
	set("path_is_worker_branch", gc.Workers.PathIsWorkerBranch)
	set("verify_has_command", gc.Progress.VerifyHasCommand)
	set("verify_declared", gc.Progress.VerifyDeclared)
	set("pending_user_input", gc.Session.PendingUserInput)
	set("stub_valid", gc.Session.StubValid)
	set("pattern_parse_ok", gc.Invocation.PatternParseOK)
	set("arg_validation_errors", strList(gc.Invocation.ArgValidationErrors))
	set("worker_leg", gc.Session.WorkerLeg)
	set("action_host_resource_denials", strList(gc.Access.ActionHostResourceDenials))
	set("confine_applied", gc.Execution.ConfineApplied)
	set("network_mode", gc.Execution.NetworkMode)
	set("denial_subject", gc.Execution.DenialSubject)
	set("confine_signals", strList(gc.Execution.ConfineSignals))
	set("failed_stages", strList(gc.Execution.FailedStages))
	set("response_tools_callable", gc.Response.ToolsCallable)
	set("response_structured_output", gc.Response.StructuredOutput)
	set("process_running", gc.Execution.ProcessRunning)
	set("ordinary_write_paths", strList(gc.Refusals.OrdinaryWritePaths))
	set("ordinary_write_grants", strList(gc.Refusals.OrdinaryWriteGrants))
	set("refused_write_paths", strList(gc.Refusals.RefusedWritePaths))
	set("refused_write_grants", strList(gc.Refusals.RefusedWriteGrants))
	set("optional_read_paths", strList(gc.Refusals.OptionalReadPaths))
	set("refused_read_paths", strList(gc.Refusals.RefusedReadPaths))
	set("refused_read_grants", strList(gc.Refusals.RefusedReadGrants))
	set("refused_multiple_read_grants", len(gc.Refusals.RefusedReadGrants) > 1)
	set("refused_credential_store_paths", strList(gc.Refusals.RefusedCredentialStorePaths))
	set("receiving_tool_summarize", gc.Invocation.Tool == "summarize")
	set("refused_terminal_read_paths", strList(gc.Refusals.RefusedTerminalReadPaths))
	set("refused_terminal_write_paths", strList(gc.Refusals.RefusedTerminalWritePaths))
	set("refused_socket_paths", strList(gc.Refusals.RefusedSocketPaths))
	set("refused_socket_host_resources", strList(gc.Refusals.RefusedSocketHostResources))
	set("refused_connect_ports", strList(gc.Refusals.RefusedConnectPorts))
	set("refused_listen_ports", strList(gc.Refusals.RefusedListenPorts))
	set("refused_signals", strList(gc.Refusals.RefusedSignals))
	set("unsandboxed_refusals", strList(gc.Refusals.UnsandboxedRefusals))
	set("worktree_stale_paths", strList(gc.Source.WorktreeStalePaths))
	set("worktree_leftover_paths", strList(gc.Source.WorktreeLeftoverPaths))
	set("worktree_conflict_paths", strList(gc.Source.WorktreeConflictPaths))
	set("tool_is_state", gc.Invocation.ToolIsState)
	set("tool_is_delegation", gc.Invocation.ToolIsDelegation)
	set("tool_is_task", gc.Invocation.ToolIsTask)
	set("tool_payload_chunkable", gc.Invocation.ToolPayloadChunkable)
	set("tool_is_handoff", gc.Invocation.ToolIsHandoff)
	set("unobserved_cited_paths", strList(gc.Grounding.UnobservedCitedPaths))
	set("unobserved_cited_urls", strList(gc.Grounding.UnobservedCitedURLs))
	set("unobserved_cited_handles", strList(gc.Grounding.UnobservedCitedHandles))
	set("claims_completion", gc.Grounding.ClaimsCompletion)
	set("has_matching_ledger_job", gc.Grounding.HasMatchingLedgerJob)
	set("worker_summary_present", gc.Grounding.WorkerSummaryPresent)
	set("worker_artifact_present", gc.Grounding.WorkerArtifactPresent)
	set("worker_artifact_measured", gc.Grounding.WorkerArtifactMeasured)
	set("repeat_count", gc.Counters.RepeatCount)
	set("fruitless_search_run", gc.Counters.FruitlessSearchRun)
	set("breaker_count", gc.Counters.BreakerCount)
	set("same_code_reject_run", gc.Counters.SameCodeRejectRun)
	set("same_call_run", gc.Counters.SameCallRun)
	set("repeated_rejection_code", gc.Counters.RepeatedRejectCode)
	set("code_reject_responses", gc.Counters.CodeRejectResponses)
	set("deferred_unactivated", gc.Counters.DeferredUnactivated)
	set("worker_attempted_mutation", gc.Workers.WorkerAttemptedMutation)
	set("batch_ready_ignoring_progress", gc.Workflow.BatchReadyIgnoringProgress)
	set("synthesis_delay_count", gc.Workflow.SynthesisDelayCount)
	set("scope_mode", gc.Workers.ScopeMode)
	set("profile_mutation_capable", gc.Workers.ProfileMutationCapable)
	set("base_overlay_id", gc.Workers.BaseOverlayID)
	set("base_overlay_resolves", gc.Workers.BaseOverlayResolves)
	set("base_overlay_checked", gc.Workers.BaseOverlayChecked)
	set("base_overlay_pending", gc.Workers.BaseOverlayPending)
	set("active_read_count", gc.Workers.ActiveReadCount)
	set("active_write_count", gc.Workers.ActiveWriteCount)
	set("max_workers", gc.Workers.MaxWorkers)
	set("max_read_workers", gc.Workers.MaxReadWorkers)
	set("max_write_workers", gc.Workers.MaxWriteWorkers)
	set("citation_unverifiable", gc.Grounding.CitationUnverifiable)
	set("scout_survey_evidence_present", gc.Grounding.ScoutSurveyEvidencePresent)
	set("surface_claim_ungrounded", gc.Grounding.SurfaceClaimUngrounded)
	set("page_measure_ungrounded", gc.Grounding.PageMeasureUngrounded)
	set("agent_is_scout", gc.Workers.AgentIsScout)
	set("agent_is_implementer", gc.Workers.AgentIsImplementer)
	set("profile_surveys_project_tree", gc.Grounding.ProfileSurveysProjectTree)
	set("ask_user_secret_allowed", false)
	set("ask_user_loadable_without_schema", false)
	set("task_callable", false)
	set("surface_snapshot_handles", []string{})
	set("repo_known_empty", gc.Grounding.RepoKnownEmpty)
	set("last_audit_ungrounded", gc.Grounding.LastAuditUngrounded)
	set("grounding_escalated", gc.Grounding.GroundingEscalated)
	set("prompt_injection_score", gc.Content.PromptInjectionScore)
	set("jailbreak_score", gc.Content.JailbreakScore)
	set("pii_entities", mapList(gc.Content.PIIEntities))
	set("secret_matches", mapList(gc.Content.SecretMatches))
	set("editorconfig_mismatch", gc.Source.EditorConfigMismatch)
	set("syntax_check_overridden", gc.Source.SyntaxCheckOverridden)
	set("source_analysis_unavailable", gc.Source.SourceAnalysisUnavailable)
	set("http_request_web_page", gc.Invocation.HTTPRequestWebPage)
	set("http_request_loopback_other_session", gc.Invocation.HTTPRequestLoopbackOtherSession)
	set("git_no_repository_on_write_leg", gc.Source.GitNoRepositoryOnWriteLeg)
	set("reproduced_source_lines", gc.Source.ReproducedSourceLines)
	set("reproduced_source_file_lines", gc.Source.ReproducedSourceFileLines)
	set("reproduced_source_path", gc.Source.ReproducedSourcePath)
	set("reproduced_source_start_line", gc.Source.ReproducedSourceStartLine)
	set("reproduced_source_end_line", gc.Source.ReproducedSourceEndLine)
	fillMCPActivation(set, gc)
	fillRejectObservationActivation(set, gc)
	fillSessionActivation(set, gc)
	fillWorkflowActivation(set, gc)
}

func fillMCPActivation(set func(string, any), gc *GuardContext) {
	set("mcp_server_id", gc.MCP.MCPServerID)
	set("mcp_tool_name", gc.MCP.MCPToolName)
	set("mcp_qualified_tool", gc.MCP.MCPQualifiedTool)
	set("mcp_server_configured", gc.MCP.MCPServerConfigured)
	set("mcp_server_enabled", gc.MCP.MCPServerEnabled)
	set("mcp_configured_servers", gc.MCP.configuredServers())
	set("mcp_enabled_servers", gc.MCP.enabledServers())
	set("mcp_result", mapOrEmpty(gc.MCP.MCPFields))
	set("mcp_call_ok", gc.MCP.MCPCallOK)
	set("mcp_error_code", gc.MCP.MCPErrorCode)
	set("mcp_schema_matched", gc.MCP.MCPSchemaMatched)
}

// fillRejectObservationActivation defines the structured tool-rejection vocabulary:
// what the host observed about a call it refused, one fact per cause.
func fillRejectObservationActivation(set func(string, any), gc *GuardContext) {
	set("command_not_argv", gc.Invocation.CommandNotArgv)
	set("is_directory", gc.Rejection.IsDirectory)
	set("not_found", gc.Rejection.NotFound)
	set("path_outside_scope", EvalPathOutsideScope(gc, gc.Invocation.Tool))
	set("path_denied", gc.Rejection.PathDenied)
	set("bulk_denied", gc.Rejection.BulkDenied)
	set("binary_denied", gc.Rejection.BinaryDenied)
	set("mode_denied", gc.Rejection.ModeDenied)
	set("path_escape", gc.Rejection.PathEscape)
	set("beyond_eof", gc.Rejection.BeyondEOF)
	set("not_running", gc.Rejection.NotRunning)
	set("unsupported", gc.Rejection.Unsupported)
	set("resource_limit", gc.Rejection.ResourceLimit)
	set("conflict", gc.Rejection.Conflict)
	set("path_required", gc.Rejection.PathRequired)
	set("id_required", gc.Rejection.IDRequired)
	set("policy_denied", gc.Rejection.PolicyDenied)
	set("unknown_target", gc.Rejection.UnknownTarget)
	set("missing", gc.Rejection.Missing)
	set("forbidden", gc.Rejection.Forbidden)
	set("selector_empty", gc.Rejection.SelectorEmpty)
	set("selector_ambiguous", gc.Rejection.SelectorAmbiguous)
	set("reject_observation", gc.Rejection.RejectObservation)
	set("rejection_code", gc.ObservedRejectCode)
}

// fillWorkflowActivation defines manifest-phase and durable workflow gate observations.
func fillWorkflowActivation(set func(string, any), gc *GuardContext) {
	set("phase", gc.Session.Phase)
	set("review_loop_active", gc.Workflow.ReviewLoopActive)
	set("review_verdict_gate_open", gc.Workflow.ReviewVerdictGateOpen)
	set("verdict_delay_count", gc.Workflow.VerdictDelayCount)
	set("closeout_gates_open", gc.Workflow.CloseoutGatesOpen)
	set("closeout_gate_open_leaves", gc.Workflow.CloseoutGateOpenLeaves)
	set("workflow_report_phase_pending", gc.Workflow.WorkflowReportPhasePending)
	set("phase_obligation_pending", gc.Workflow.PhaseObligationPending)
	set("phase_obligation_kinds", gc.Workflow.PhaseObligationKinds)
}

// fillSessionActivation projects the core profile, counters, caller identity,
// content provenance, and argument diagnostics.
func fillSessionActivation(set func(string, any), gc *GuardContext) {
	set("anchor", gc.Anchor)
	set("fire_count", gc.Counters.FireCount)
	set("credential_decisions", gc.Counters.CredentialDecisions)
	set("tool_profile", gc.Session.PermissionProfile)
	set("principal", gc.Session.Principal)
	set("principal_roles", strList(gc.Session.PrincipalRoles))
	set("content_length", gc.Content.ContentLength)
	set("content_roles", strList(gc.Content.ContentRoles))
	set("content_origins", strList(gc.Content.ContentOrigins))
	set("content_authorities", strList(gc.Content.ContentAuthorities))
	set("content_trust_tiers", strList(gc.Content.ContentTrustTiers))
	set("content_sources", strList(gc.Content.ContentSources))
	set("content_segment_count", gc.Content.ContentSegmentCount)
	set("content_contains_untrusted", gc.Content.ContentContainsUntrusted)
	set("arg_validation_reason", gc.Invocation.ArgValidationReason)
	set("arg_validation_field", gc.Invocation.ArgValidationField)
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
