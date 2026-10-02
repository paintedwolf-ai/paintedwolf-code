package settings

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/isolation"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

// RuleApprovalGate evaluates approval policy for permitted tool actions.
type RuleApprovalGate struct {
	store *ApprovalStore
	// grants holds explicit chat leases.
	grants *sessionGrants
	// quiets holds chat-keyed ask suppressions.
	quiets *askQuiets
	// sources are fixed at construction.
	sources Sources
}

// NewRuleApprovalGate constructs a settings-backed approval gate.
func NewRuleApprovalGate(store *ApprovalStore, sources Sources) hitl.ApprovalGate {
	return &RuleApprovalGate{
		store: store, grants: newSessionGrants(), quiets: newAskQuiets(),
		sources: sources.normalized(),
	}
}

// Evaluate applies enforcement before discretionary approval policy.
func (g *RuleApprovalGate) Evaluate(ctx context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	if g.store == nil {
		return &hitl.ApprovalResult{}, nil
	}

	scope, ref := approvalScopeRef(action)
	cfg := g.store.Get(scope, ref)
	layers := EffectiveApprovalRuleLayers(ctx, g.store, g.sources.ApprovalRules, scope, ref)

	// The path resolver applies the same predicate, so no card offers what it refuses.
	if res := controlPlaneDenyResult(action); res != nil {
		return res, nil
	}

	// Deny rules remain active when prompts are disabled.
	if rule, ok := denyRuleForLayers(layers, action); ok {
		return deniedByRules(
			matchingDenyRules(layers, action), rule, action, CommandTextFromActionArgs(action.Args),
		), nil
	}

	// Bypass settings affect discretionary asks after enforcement.
	if confine.BypassEnabled() {
		return &hitl.ApprovalResult{}, nil
	}

	// NeverAsk disables discretionary prompts only.
	if cfg.NeverAsk != nil && *cfg.NeverAsk {
		return &hitl.ApprovalResult{}, nil
	}
	facts, citation, matchedRules := g.factsForAction(action, cfg, layers)
	verdict, decision := gate.Evaluate(facts, cfg.Posture)
	res := &hitl.ApprovalResult{DetectionCitation: citation}
	if verdict != gate.Ask {
		return res, nil
	}
	res.Decision = decision
	res.FileAccess = g.currentActionFileAccess(action)
	res.MatchedRules = approvalRuleMatches(matchedRules, action, CommandTextFromActionArgs(action.Args))
	res.HostResourceApproval = hostResourceAskPending(g, cfg, layers, action)
	if !facts.Leased && g.grants != nil && g.grants.hasExactActionSet(action.ChatSession()) {
		res.GrantDelta = "This action was not in the approved action set."
	}
	return res, nil
}

// deniedByRules renders an enforcement block with rule citations.
func deniedByRules(matches []ApprovalRule, primary ApprovalRule, action hitl.ProposedAction, command string) *hitl.ApprovalResult {
	if len(matches) == 0 {
		matches = []ApprovalRule{primary}
	}
	return &hitl.ApprovalResult{
		Denied: true, DenyCode: "APPROVAL_RULE_DENIED",
		MatchedRules: approvalRuleMatches(matches, action, command),
	}
}

func approvalRuleMatches(rules []ApprovalRule, action hitl.ProposedAction, command string) []hitl.ApprovalRuleMatch {
	out := make([]hitl.ApprovalRuleMatch, 0, len(rules))
	for _, rule := range rules {
		match := hitl.ApprovalRuleMatch{
			Category: string(rule.Category), Pattern: rule.Pattern, Effect: string(rule.Effect),
			UnitID: rule.Source.UnitID, PackID: rule.Source.PackID, Scope: string(rule.Source.Scope),
		}
		if rule.Category == ApprovalCategoryCommand && command != "" && IsCommandToolName(action.Tool) {
			match.Command = command
		}
		out = append(out, match)
	}
	return out
}

// hostResourceAskPending reports an uncovered host-resource ask.
func hostResourceAskPending(g *RuleApprovalGate, cfg ApprovalConfig, layers ApprovalRuleLayers, action hitl.ProposedAction) bool {
	device, deviceOK := matchHostResourceRules(layers.Device, action.HostResources, action.HostResourceFamilies)
	project, projectOK := matchHostResourceRules(layers.Project, action.HostResources, action.HostResourceFamilies)
	if (!deviceOK || device.Effect != ApprovalEffectAsk) && (!projectOK || project.Effect != ApprovalEffectAsk) {
		return false
	}
	return !hostResourceGrantCovers(g, cfg.Grants, action)
}

func denyRuleForLayers(layers ApprovalRuleLayers, action hitl.ProposedAction) (ApprovalRule, bool) {
	if rules := effectiveDenyRulesForLayer(layers.Device, action); len(rules) > 0 {
		return rules[0], true
	}
	if rules := effectiveDenyRulesForLayer(layers.Project, action); len(rules) > 0 {
		return rules[0], true
	}
	return ApprovalRule{}, false
}

func matchingDenyRules(layers ApprovalRuleLayers, action hitl.ProposedAction) []ApprovalRule {
	var out []ApprovalRule
	for _, rules := range [][]ApprovalRule{layers.Device, layers.Project} {
		out = append(out, effectiveDenyRulesForLayer(rules, action)...)
	}
	return out
}

// ForgetSession drops all remembered approvals and quiets for a chat session.
func (g *RuleApprovalGate) ForgetSession(chatSessionID string) {
	if g.grants != nil {
		g.grants.forget(chatSessionID)
	}
	if g.quiets != nil {
		g.quiets.forget(chatSessionID)
	}
}

// PutAskQuiet installs a chat-keyed ask quiet. ttlSeconds 0 means until revoked.
func (g *RuleApprovalGate) PutAskQuiet(q hitl.AskQuiet, ttlSeconds int) (hitl.AskQuiet, bool) {
	if g == nil || g.quiets == nil {
		return hitl.AskQuiet{}, false
	}
	return g.quiets.put(q, ttlSeconds)
}

// AskQuietLive reports whether key is currently quieted for the chat.
func (g *RuleApprovalGate) AskQuietLive(chatSessionID, key string) (hitl.AskQuiet, bool) {
	if g == nil || g.quiets == nil {
		return hitl.AskQuiet{}, false
	}
	return g.quiets.live(chatSessionID, key)
}

// NoteAskQuietSuppressed increments the suppressed counter on a live quiet.
func (g *RuleApprovalGate) NoteAskQuietSuppressed(chatSessionID, key string) {
	if g == nil || g.quiets == nil {
		return
	}
	g.quiets.noteSuppressed(chatSessionID, key)
}

// ListAskQuiets returns live quiets for chat, or all chats when chat is empty.
func (g *RuleApprovalGate) ListAskQuiets(chatSessionID string) []hitl.AskQuiet {
	if g == nil || g.quiets == nil {
		return nil
	}
	return g.quiets.list(chatSessionID)
}

// RevokeAskQuiet drops one quiet by id. Returns false when unknown.
func (g *RuleApprovalGate) RevokeAskQuiet(id string) bool {
	if g == nil || g.quiets == nil {
		return false
	}
	return g.quiets.revoke(id)
}

// RevokeAskQuietInstalledBy drops a quiet only when its installing operation still identifies it.
func (g *RuleApprovalGate) RevokeAskQuietInstalledBy(id, operationID string) bool {
	if g == nil || g.quiets == nil {
		return false
	}
	return g.quiets.revokeInstalledBy(id, operationID)
}

// winningRule prefers deny over ask, then the most specific pattern, so
// specificity only decides which of several asks the card cites.
func winningRule(matches []ApprovalRule) (ApprovalRule, bool) {
	if len(matches) == 0 {
		return ApprovalRule{}, false
	}
	best := matches[0]
	for _, r := range matches[1:] {
		if rulePrecedenceLess(best, r) {
			best = r
		}
	}
	return best, true
}

// rulePrecedenceLess reports whether a loses to b: effect first, then specificity.
func rulePrecedenceLess(a, b ApprovalRule) bool {
	if ae, be := effectPrecedence(a.Effect), effectPrecedence(b.Effect); ae != be {
		return ae < be
	}
	return patternSpecificity(a.Pattern) < patternSpecificity(b.Pattern)
}

// patternSpecificity counts literal characters in a glob.
func patternSpecificity(pattern string) int {
	n := 0
	for _, r := range strings.TrimSpace(pattern) {
		if r != '*' && r != '?' {
			n++
		}
	}
	return n
}

func effectPrecedence(e ApprovalEffect) int {
	switch e {
	case ApprovalEffectDeny:
		return 2
	case ApprovalEffectAsk:
		return 1
	default:
		return 0
	}
}

// matchCommandRules returns the winning rule and matching command unit.
func matchCommandRules(rules []ApprovalRule, units []string) (ApprovalRule, string, bool) {
	var matches []ApprovalRule
	matchedBy := make(map[string]string, len(rules))
	for _, r := range rules {
		if r.Category != ApprovalCategoryCommand {
			continue
		}
		for _, unit := range units {
			if !MatchCommandPattern(unit, r.Pattern) {
				continue
			}
			matches = append(matches, r)
			matchedBy[r.Pattern] = unit
			break
		}
	}
	rule, ok := winningRule(matches)
	if !ok {
		return ApprovalRule{}, "", false
	}
	return rule, matchedBy[rule.Pattern], true
}

func matchGeneralRules(rules []ApprovalRule, action hitl.ProposedAction) (ApprovalEffect, ApprovalRule, bool) {
	var matches []ApprovalRule
	for _, r := range rules {
		if r.Category == ApprovalCategoryCommand || r.Category == ApprovalCategoryHostResource {
			continue
		}
		if ruleMatches(r, action) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return "", ApprovalRule{}, false
	}
	best := matches[0]
	for _, r := range matches[1:] {
		if generalRulePrecedenceLess(best, r, action) {
			best = r
		}
	}
	return best.Effect, best, true
}

// generalRulePrecedenceLess keeps deny first, then ranks exact tools above
// globs and tool classes.
func generalRulePrecedenceLess(a, b ApprovalRule, action hitl.ProposedAction) bool {
	if ae, be := effectPrecedence(a.Effect), effectPrecedence(b.Effect); ae != be {
		return ae < be
	}
	if a.Category == ApprovalCategoryTool && b.Category == ApprovalCategoryTool {
		ar, br := toolMatchRank(a, action), toolMatchRank(b, action)
		if ar != br {
			return ar < br
		}
	}
	return rulePrecedenceLess(a, b)
}

func toolMatchRank(r ApprovalRule, action hitl.ProposedAction) int {
	pattern, tool := strings.TrimSpace(r.Pattern), strings.TrimSpace(action.Tool)
	switch {
	case pattern == tool:
		return 2
	case matchGlob(pattern, tool):
		return 1
	default:
		return 0
	}
}

// EvaluateHostResourceSubjects resolves policy across resource IDs and families.
func EvaluateHostResourceSubjects(rules []ApprovalRule, subjects []string) (ApprovalEffect, ApprovalRule, bool) {
	var matches []ApprovalRule
	for _, rule := range rules {
		if rule.Category != ApprovalCategoryHostResource {
			continue
		}
		for _, subject := range subjects {
			if matchGlob(rule.Pattern, subject) {
				matches = append(matches, rule)
				break
			}
		}
	}
	rule, ok := winningRule(matches)
	if !ok {
		return "", ApprovalRule{}, false
	}
	return rule.Effect, rule, true
}

// ExactHostResourceRule returns the rule stored for one resource ID.
func ExactHostResourceRule(rules []ApprovalRule, resourceID string) (ApprovalEffect, bool) {
	var matches []ApprovalRule
	for _, rule := range rules {
		if rule.Category == ApprovalCategoryHostResource && rule.Pattern == resourceID {
			matches = append(matches, rule)
		}
	}
	rule, ok := winningRule(matches)
	return rule.Effect, ok
}

func matchHostResourceRules(rules []ApprovalRule, ids, families []string) (ApprovalRule, bool) {
	subjects := append(append([]string(nil), ids...), families...)
	_, rule, ok := EvaluateHostResourceSubjects(rules, subjects)
	return rule, ok
}

func ruleMatches(r ApprovalRule, action hitl.ProposedAction) bool {
	switch r.Category {
	case ApprovalCategoryTool:
		return matchToolPattern(r.Pattern, action.Tool)
	case ApprovalCategoryMCP:
		if !ingestion.IsMCPToolName(strings.TrimSpace(action.Tool)) {
			return false
		}
		if action.ApprovalCategory == string(ApprovalCategoryMCP) && strings.TrimSpace(action.ApprovalSubject) != "" {
			return matchGlob(r.Pattern, strings.TrimSpace(action.ApprovalSubject))
		}
		return false
	case ApprovalCategoryPath:
		for _, f := range action.Files {
			if matchGlob(r.Pattern, f) || matchGlob(r.Pattern, filepath.Base(f)) {
				return true
			}
		}
		return false
	case ApprovalCategoryHostResource:
		for _, id := range append(append([]string(nil), action.HostResources...), action.HostResourceFamilies...) {
			if matchGlob(r.Pattern, id) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func matchToolPattern(pattern, tool string) bool {
	pattern = strings.TrimSpace(pattern)
	tool = strings.TrimSpace(tool)
	if pattern == "" || tool == "" {
		return false
	}
	if pattern == tool {
		return true
	}
	if matchGlob(pattern, tool) {
		return true
	}
	return matchToolClass(pattern, tool)
}

// ToolClassWrite is the approval-rule pattern naming every tool that writes a
// declared path. Membership is the catalog's mutates_path axis.
const ToolClassWrite = "write"

// matchToolClass resolves catalog-defined tool classes.
func matchToolClass(pattern, tool string) bool {
	if pattern == ToolClassWrite {
		return IsPathMutatingTool(tool)
	}
	return false
}

// matchGlob supports recursive path patterns.
func matchGlob(pattern, value string) bool {
	if strings.TrimSpace(pattern) == "*" {
		return true
	}
	return pathglob.Match(pattern, value)
}

// Tools omitted from pathMutatingSet are treated as read-only.
var pathMutatingSet = tierSet(pathMutatingTools)

// IsPathMutatingTool reports whether a tool writes a path its own arguments
// declare.
func IsPathMutatingTool(tool string) bool {
	_, ok := pathMutatingSet[strings.TrimSpace(tool)]
	return ok
}

// controlPlaneDenyResult blocks a declared path inside the host's own state
// tree. Command tools declare no paths here; confinement answers for them.
func controlPlaneDenyResult(action hitl.ProposedAction) *hitl.ApprovalResult {
	if IsCommandToolName(action.Tool) {
		return nil
	}
	write := IsPathMutatingTool(action.Tool)
	for _, f := range action.Files {
		path := strings.TrimSpace(f)
		if !filepath.IsAbs(path) {
			continue
		}
		if confine.ControlPlanePathDenied(path, write, action.SessionScratchRoot) {
			return &hitl.ApprovalResult{
				Denied:      true,
				DenyCode:    isolation.CodeControlPlaneDenied,
				DenySubject: path,
			}
		}
	}
	return nil
}
