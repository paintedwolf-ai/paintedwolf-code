package oar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oarcopy"
)

// ConformanceFixture is a production-engine fixture.
type ConformanceFixture struct {
	Name     string          `json:"name,omitempty"`
	Rule     json.RawMessage `json:"rule"`
	Input    FixtureInput    `json:"input"`
	Expected FixtureExpected `json:"expected"`
}

// FixtureInput is the GuardContext subset supplied to the runner.
type FixtureInput struct {
	Anchor string         `json:"anchor,omitempty"`
	Stage  string         `json:"stage,omitempty"`
	Facts  map[string]any `json:"facts"`
}

// FixtureExpected is the expected Decision.
type FixtureExpected struct {
	Decision string   `json:"decision"` // block|warn|nudge|allow|none
	Code     string   `json:"code,omitempty"`
	OnFire   []string `json:"on_fire,omitempty"`
}

// ConformanceRunner evaluates production-engine fixtures.
type ConformanceRunner struct {
	loader *Loader
}

// NewConformanceRunner builds a runner. schemaDir may be empty to skip JSON Schema.
func NewConformanceRunner(schemaDir string) (*ConformanceRunner, error) {
	l, err := NewLoader(schemaDir)
	if err != nil {
		return nil, err
	}
	return &ConformanceRunner{loader: l}, nil
}

// LoadFixturesDir loads *.json fixtures from dir.
func LoadFixturesDir(dir string) ([]ConformanceFixture, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ConformanceFixture
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var fx ConformanceFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if fx.Name == "" {
			fx.Name = strings.TrimSuffix(e.Name(), ".json")
		}
		out = append(out, fx)
	}
	return out, nil
}

// RunFixture evaluates one fixture and returns an error on mismatch.
func (cr *ConformanceRunner) RunFixture(fx ConformanceFixture) error {
	if cr == nil || cr.loader == nil {
		return fmt.Errorf("conformance runner not initialized")
	}
	rule, err := cr.parseRuleJSON(fx.Rule)
	if err != nil {
		return fmt.Errorf("rule: %w", err)
	}
	rs := NewRuleSet([]*Rule{rule})
	stage := Stage(fx.Input.Stage)
	if stage == "" {
		stage = StageFromAnchor(rule.Anchor)
	}
	p := NewGuardPipeline(rs, cr.loader, NewCounterStore())
	p.SetEventPublisher(acceptPublisher{})
	gc := factsToContext(fx.Input.Facts)
	if fx.Input.Anchor != "" {
		rule.Anchor = fx.Input.Anchor
	}
	var res *PipelineResult
	if fx.Input.Anchor != "" {
		p.EnableAnchor(fx.Input.Anchor)
		res, err = p.EvaluateBlock(context.Background(), fx.Input.Anchor, gc)
	} else {
		res, err = p.Evaluate(context.Background(), stage, gc)
	}
	if err != nil {
		return err
	}
	return checkExpected(fx.Expected, res)
}

func (cr *ConformanceRunner) parseRuleJSON(raw json.RawMessage) (*Rule, error) {
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	doc, ok := probe.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("[OAR-DOC-31] a rule set member is not a rule document: expected a JSON object")
	}
	// Validate raw keys before parsing can discard unknown fields.
	if err := cr.loader.ValidateDocument(raw); err != nil {
		return nil, err
	}
	// Reuse the loader path via a YAML-shaped map.
	code, _ := doc["id"].(string)
	if code == "" {
		return nil, fmt.Errorf("rule id required")
	}
	rule, skip, err := cr.loader.parseRule(code, doc)
	if err != nil {
		return nil, err
	}
	if skip {
		return nil, fmt.Errorf("[OAR-DOC-31] rule set member %s is missing oar", code)
	}
	return rule, nil
}

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
	if v, ok := facts["profile"].(string); ok {
		gc.Session.Profile = v
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
	setBool("pack_runner_task", &gc.Invocation.PackRunnerTask)
	setBool("posture_unresolved", &gc.Session.PostureUnresolved)
	setBool("agent_is_plan_writer", &gc.Workers.AgentIsPlanWriter)
	setBool("disallowed_agent", &gc.Workers.DisallowedAgent)
	setBool("plan_awaiting_approval", &gc.Workflow.PlanAwaitingApproval)
	setBool("path_outside_scope", &gc.Access.PathOutsideScope)
	// Core observations a published fixture may set directly.
	setBool("path_denied", &gc.Rejection.PathDenied)
	setBool("not_found", &gc.Rejection.NotFound)
	setBool("is_directory", &gc.Rejection.IsDirectory)
	setBool("policy_denied", &gc.Rejection.PolicyDenied)
	setBool("content_contains_untrusted", &gc.Content.ContentContainsUntrusted)
	if v, ok := facts["tool_args_fingerprint"].(string); ok {
		gc.Invocation.ToolArgsFingerprint = v
	}
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
	if v, ok := facts["same_code_reject_run"].(float64); ok {
		gc.Counters.SameCodeRejectRun = int64(v)
	}
	if v, ok := facts["prompt_injection_score"].(float64); ok {
		gc.Content.PromptInjectionScore = v
	}
	if v, ok := facts["jailbreak_score"].(float64); ok {
		gc.Content.JailbreakScore = v
	}
	if v, ok := facts["breaker_count"].(float64); ok {
		gc.Counters.BreakerCount = int64(v)
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
	if v, ok := facts["permission_profile"].(string); ok {
		gc.Session.PermissionProfile = v
	}
	if v, ok := facts["anchor"].(string); ok {
		gc.Anchor = v
	}
	gc.DeriveToolClassFacts()
	gc.Published = make(map[string]any, len(facts))
	for k, v := range facts {
		gc.Published[k] = v
		if !strings.HasPrefix(k, oarcopy.HostFactNamespace+".") {
			if factTiers()[k] == FactTierHost {
				gc.Published[publishedName(k, FactTierHost)] = v
			}
		}
	}
	return gc
}

func checkExpected(exp FixtureExpected, res *PipelineResult) error {
	want := strings.ToLower(strings.TrimSpace(exp.Decision))
	if want == "" || want == "none" || want == "allow" {
		if res != nil && res.Decision != nil {
			d := res.Decision
			if d.Effect == EffectBlock || d.Effect == EffectNudge || d.Effect == EffectWarn {
				return fmt.Errorf("expected no actionable decision, got %s %s", d.Effect, d.Code)
			}
		}
		return nil
	}
	if res == nil || res.Decision == nil {
		return fmt.Errorf("expected decision %s, got none", want)
	}
	d := res.Decision
	if string(d.Effect) != want {
		return fmt.Errorf("expected effect %s, got %s (%s)", want, d.Effect, d.Code)
	}
	if exp.Code != "" && d.Code != exp.Code {
		return fmt.Errorf("expected code %s, got %s", exp.Code, d.Code)
	}
	if len(exp.OnFire) > 0 {
		got := map[string]struct{}{}
		for _, a := range d.OnFire {
			got[string(a)] = struct{}{}
		}
		for _, wantA := range exp.OnFire {
			if _, ok := got[wantA]; !ok {
				return fmt.Errorf("expected on_fire %s, got %v", wantA, d.OnFire)
			}
		}
	}
	return nil
}

// SeedFixturesFromScenarios writes fixtures for conditioned hint scenarios.
func SeedFixturesFromScenarios(policyDir, outDir string, codes []string) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	if strings.TrimSpace(policyDir) == "" {
		if err := anchorcatalog.InstallBundled(); err != nil {
			return fmt.Errorf("install catalog: %w", err)
		}
	} else {
		// …/packs/painted-wolf/<leaf>/policy → climb to painted-wolf/platform/host/anchors
		catalogPath := filepath.Join(policyDir, "..", "..", "platform", "host", "anchors", "catalog.yaml")
		if err := anchorcatalog.InstallFile(catalogPath); err != nil {
			return fmt.Errorf("install catalog: %w", err)
		}
	}
	l, err := NewLoader("")
	if err != nil {
		return err
	}
	var rs *RuleSet
	var loadErr error
	// An empty path selects shipped policy; a supplied path selects host rule YAML.
	if strings.TrimSpace(policyDir) == "" {
		rs, loadErr = l.LoadEffectivePolicy()
	} else {
		rs, loadErr = l.LoadDir(extpacks.OnDisk(policyDir))
	}
	if loadErr != nil {
		return loadErr
	}
	for _, code := range codes {
		r, ok := rs.Get(code)
		if !ok || r.When == "" {
			continue
		}
		fx := ConformanceFixture{
			Name: code + "_default",
			Rule: mustRuleJSON(r),
			Input: FixtureInput{
				Anchor: r.Anchor,
				Stage:  string(StageFromAnchor(r.Anchor)),
				Facts: map[string]any{
					"tool":            firstTool(r),
					"session_posture": "spec",
				},
			},
			Expected: FixtureExpected{
				Decision: string(r.Effect),
				Code:     r.ID,
			},
		}
		if r.Anchor == AnchorToolRejected {
			fx.Input.Facts["paintedwolf.rejection_code"] = code
		}
		raw, err := json.MarshalIndent(fx, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		path := filepath.Join(outDir, code+".json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func firstTool(r *Rule) string {
	if tools := r.Selector["tool"]; len(tools) > 0 {
		return tools[0]
	}
	return ""
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

func mustRuleJSON(r *Rule) json.RawMessage {
	doc := map[string]any{
		"oar":    r.OAR,
		"id":     r.ID,
		"kind":   string(r.Kind),
		"anchor": r.Anchor,
		"effect": string(r.Effect),
	}
	if r.When != "" {
		doc["when"] = r.When
	}
	if len(r.Selector) > 0 {
		selector := map[string]any{}
		for _, clause := range r.Selector.Clauses() {
			values := r.Selector[clause]
			arr := make([]any, len(values))
			for i, v := range values {
				arr[i] = v
			}
			selector[clause] = arr
		}
		doc["selector"] = selector
	}
	requires := map[string]any{}
	if len(r.Requires.Profiles) > 0 {
		arr := make([]any, len(r.Requires.Profiles))
		for i, p := range r.Requires.Profiles {
			arr[i] = p
		}
		requires["profiles"] = arr
	}
	if len(r.Requires.Facts) > 0 {
		arr := make([]any, len(r.Requires.Facts))
		for i, f := range r.Requires.Facts {
			arr[i] = f
		}
		requires["facts"] = arr
	}
	if len(requires) > 0 {
		doc["requires"] = requires
	}
	if r.Emit != "" {
		doc["x-paintedwolf-emit"] = r.Emit
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return raw
}

// ErrRuleNotEligible marks a document the loader skipped as not an OAR rule.
var ErrRuleNotEligible = errors.New("not OAR-eligible")
