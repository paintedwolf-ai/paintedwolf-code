package oar

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oarcopy"
)

// factsToContext places a fixture's facts on a guard context: field-backed
// observations get their fields, and every fact is published by name.
func factsToContext(facts map[string]any) *GuardContext {
	gc := NewGuardContext()
	if facts == nil {
		return gc
	}
	facts = fixtureFactBindings(facts)
	if v, ok := facts["tool"].(string); ok {
		gc.Invocation.Tool = v
	}
	if v, ok := facts["session_posture"].(string); ok {
		gc.Session.SessionPosture = v
	}
	if v, ok := facts["tool_profile"].(string); ok {
		gc.Session.PermissionProfile = v
	}
	if v, ok := facts["session_role"].(string); ok {
		gc.Session.Role = v
	}
	if v, ok := facts["surface"].(string); ok {
		gc.Session.Surface = v
	}
	if v, ok := facts["phase"].(string); ok {
		gc.Session.Phase = v
	}
	if v, ok := facts["session_id"].(string); ok {
		gc.Session.SessionID = v
	}
	setStringList := func(key string, dst *[]string) {
		list, ok := facts[key].([]any)
		if !ok {
			return
		}
		*dst = (*dst)[:0]
		for _, value := range list {
			if text, ok := value.(string); ok {
				*dst = append(*dst, text)
			}
		}
	}
	setStringList("content_roles", &gc.Content.ContentRoles)
	setStringList("content_origins", &gc.Content.ContentOrigins)
	setStringList("content_authorities", &gc.Content.ContentAuthorities)
	setStringList("content_trust_tiers", &gc.Content.ContentTrustTiers)
	setStringList("content_sources", &gc.Content.ContentSources)
	if v, ok := facts["content_segment_count"].(float64); ok {
		gc.Content.ContentSegmentCount = int64(v)
	}
	setBool := func(key string, dst *bool) {
		if v, ok := facts[key].(bool); ok {
			*dst = v
		}
	}
	setBool("workers_idle", &gc.Workers.WorkersIdle)
	setBool("claims_completion", &gc.Grounding.ClaimsCompletion)
	setBool("has_matching_ledger_job", &gc.Grounding.HasMatchingLedgerJob)
	setBool("stub_valid", &gc.Session.StubValid)
	setBool("tool_is_state", &gc.Invocation.ToolIsState)
	setBool("tool_is_task", &gc.Invocation.ToolIsTask)
	setBool("tool_payload_chunkable", &gc.Invocation.ToolPayloadChunkable)
	setBool("tool_is_delegation", &gc.Invocation.ToolIsDelegation)
	setBool("tool_is_handoff", &gc.Invocation.ToolIsHandoff)
	setBool("path_outside_scope", &gc.Access.PathOutsideScope)
	// Filesystem profile and host observations a fixture may set directly.
	setBool("path_denied", &gc.Rejection.PathDenied)
	setBool("not_found", &gc.Rejection.NotFound)
	setBool("is_directory", &gc.Rejection.IsDirectory)
	setBool("policy_denied", &gc.Rejection.PolicyDenied)
	setBool("content_contains_untrusted", &gc.Content.ContentContainsUntrusted)
	if m, ok := facts["tool_args"].(map[string]any); ok {
		gc.Invocation.ToolArgs = m
	}
	if list, ok := facts["arg_validation_errors"].([]any); ok {
		gc.Invocation.ArgValidationErrors = nil
		for _, v := range list {
			if s, ok := v.(string); ok {
				gc.Invocation.ArgValidationErrors = append(gc.Invocation.ArgValidationErrors, s)
			}
		}
	}
	if v, ok := facts["prompt_injection_score"].(float64); ok {
		gc.Content.PromptInjectionScore = v
	}
	if v, ok := facts["jailbreak_score"].(float64); ok {
		gc.Content.JailbreakScore = v
	}
	if v, ok := facts["repeat_count"].(float64); ok {
		gc.Counters.RepeatCount = int64(v)
	}
	if v, ok := facts["deferred_unactivated"].(float64); ok {
		gc.Counters.DeferredUnactivated = int64(v)
	}
	if m, ok := facts["path_outside_scope_by_tool"].(map[string]any); ok {
		gc.Access.PathOutsideScopeByTool = map[string]bool{}
		for k, v := range m {
			if b, ok := v.(bool); ok {
				gc.Access.PathOutsideScopeByTool[k] = b
			}
		}
	}
	if v, ok := facts["anchor"].(string); ok {
		gc.Anchor = v
	}
	gc.DeriveToolClassFacts()
	gc.Published = make(map[string]any, len(facts))
	for k, v := range facts {
		gc.Published[k] = v
		if !strings.HasPrefix(k, oarcopy.HostFactNamespace+".") {
			if bareFactTiers[k] == FactTierHost {
				gc.Published[publishedName(k, FactTierHost)] = v
			}
		}
	}
	return gc
}

// fixtureFactBindings exposes published host facts to field-backed observations.
func fixtureFactBindings(facts map[string]any) map[string]any {
	out := make(map[string]any, len(facts))
	for name, value := range facts {
		out[name] = value
	}
	for name, value := range facts {
		bare := BareFactName(name)
		if bare == name {
			continue
		}
		if _, taken := out[bare]; !taken {
			out[bare] = value
		}
	}
	return out
}
