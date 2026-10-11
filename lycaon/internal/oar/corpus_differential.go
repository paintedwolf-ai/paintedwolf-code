package oar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
	"gopkg.in/yaml.v3"
)

// Differential corpus run: the published conformance corpus, evaluated by the
// production engine. A fixture this engine cannot carry exactly is skipped
// with a reason. A skipped fixture is not a passed fixture ([OAR-CONF-24]).

// CorpusOutcome is one fixture's verdict from the production engine.
type CorpusOutcome struct {
	ID     string         `json:"id"`
	Status string         `json:"status"` // pass | fail | skip
	Reason string         `json:"reason,omitempty"`
	Actual map[string]any `json:"actual,omitempty"`
}

type corpusToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type corpusFixture struct {
	ID    string            `json:"id"`
	Rules []json.RawMessage `json:"rules"`
	Input struct {
		ToolCall       *corpusToolCall `json:"tool_call"`
		Capability     json.RawMessage `json:"capability"`
		Anchor         string          `json:"anchor"`
		SessionID      string          `json:"session_id"`
		Facts          map[string]any  `json:"facts"`
		DetectorFacts  map[string]any  `json:"detector_facts"`
		Content        *string         `json:"content"`
		RecentActivity []string        `json:"recent_activity"`
		Config         []any           `json:"config"`
		Occurrences    []corpusOcc     `json:"occurrences"`
	} `json:"input"`
	RawInput map[string]json.RawMessage `json:"-"`
	Expected map[string]any             `json:"expected"`
}

type corpusOcc struct {
	ToolCall       *corpusToolCall `json:"tool_call"`
	DetectorFacts  map[string]any  `json:"detector_facts"`
	Anchor         string          `json:"anchor"`
	Facts          map[string]any  `json:"facts"`
	RecentActivity []string        `json:"recent_activity"`
	Content        *string         `json:"content"`
	Expected       map[string]any  `json:"expected"`
}

// RunCorpusDir evaluates every fixture in dir through the production engine.
func RunCorpusDir(dir string) ([]CorpusOutcome, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("corpus dir: %w", err)
	}
	var out []CorpusOutcome
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") || name == "manifest.json" || name == "exempt.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var fx corpusFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var envelope struct {
			Input map[string]json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		fx.RawInput = envelope.Input
		if strings.TrimSpace(fx.ID) == "" {
			fx.ID = strings.TrimSuffix(name, ".json")
		}
		out = append(out, RunCorpusFixture(fx))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// RunCorpusFixture evaluates one fixture, or explains why it cannot.
func RunCorpusFixture(fx corpusFixture) CorpusOutcome {
	prep := prepareCorpusFixture(fx)
	if prep.done {
		return prep.outcome
	}
	return evaluateCorpusOccurrences(fx, prep.ready)
}

// corpusPrepared is a fixture the production engine can evaluate: rules
// loaded, identities resolved, pipeline ready.
type corpusPrepared struct {
	capDoc      *CapabilityDocument
	rs          *RuleSet
	pipeline    *GuardPipeline
	store       *CounterStore
	sessionID   string
	occurrences []corpusOcc
}

type corpusPrepResult struct {
	ready   corpusPrepared
	outcome CorpusOutcome
	done    bool
}

func prepareCorpusFixture(fx corpusFixture) corpusPrepResult {
	if outcome, skip := corpusUnknownInputSkip(fx); skip {
		return corpusPrepResult{outcome: outcome, done: true}
	}

	capDoc, err := parseFixtureCapability(fx.Input.Capability)
	if err != nil {
		return corpusExpectedError(fx, err, "capability")
	}

	runner, err := NewConformanceRunner("")
	if err != nil {
		return corpusDone(failCorpus(fx, "runner: "+err.Error()))
	}
	runner.loader.SetCapability(capDoc)
	detectors := corpusReservedDetectors()
	if fx.Input.DetectorFacts != nil {
		detectors.Register(fixtureFactsDetector{facts: fx.Input.DetectorFacts})
	}
	runner.loader.SetDetectors(detectors)

	rules, outcome, ok := parseCorpusRules(fx, runner)
	if !ok {
		return corpusDone(outcome)
	}

	rs := NewRuleSet(rules)
	if outcome, done := validateCorpusRuleSet(fx, rs); done {
		return corpusDone(outcome)
	}
	rs, outcome, done := applyCorpusOperatorConfig(fx, rs)
	if done {
		return corpusDone(outcome)
	}

	store := NewCounterStore()
	p := NewGuardPipeline(rs, runner.loader, store)
	p.SetDetectors(detectors)
	p.SetEventPublisher(acceptPublisher{})
	sessionID := strings.TrimSpace(fx.Input.SessionID)
	if sessionID == "" {
		sessionID = "fixture"
	}
	return corpusPrepResult{ready: corpusPrepared{
		capDoc: capDoc, rs: rs, pipeline: p, store: store,
		sessionID:   sessionID,
		occurrences: corpusOccurrences(fx),
	}}
}

func evaluateCorpusOccurrences(fx corpusFixture, prepared corpusPrepared) CorpusOutcome {
	actual := map[string]any{}
	for i, occ := range prepared.occurrences {
		local := strings.TrimSpace(occ.Anchor)
		if local == "" {
			return failCorpus(fx, "fixture declares no anchor")
		}
		gc := factsToContext(occ.Facts)
		gc.Session.SessionID = prepared.sessionID
		if occ.ToolCall != nil {
			gc.ObserveToolCall(occ.ToolCall.Name, occ.ToolCall.Arguments)
			if tool, ok := occ.Facts["tool"].(string); ok {
				gc.Invocation.Tool = tool
			}
			if args, ok := occ.Facts["tool_args"].(map[string]any); ok {
				gc.Invocation.ToolArgs = args
			}
		}
		gc.Session.RecentToolNames = occ.RecentActivity
		if occ.Content != nil {
			gc.Content.Content = *occ.Content
			gc.Content.ContentSet = true
			gc.Content.ContentLength = int64(len([]rune(gc.Content.Content)))
		}
		detectors := corpusReservedDetectors()
		if occ.DetectorFacts != nil {
			detectors.Register(fixtureFactsDetector{facts: occ.DetectorFacts})
		}
		prepared.pipeline.SetDetectors(detectors)
		prepared.pipeline.EnableAnchor(local)
		res, err := prepared.pipeline.EvaluateBlock(context.Background(), local, gc)
		if err != nil {
			result := failCorpus(fx, "evaluate: "+err.Error())
			result.Actual = map[string]any{"error": err.Error()}
			return result
		}
		actual = reportCorpusResult(res, prepared.store, prepared.sessionID)

		expected := occ.Expected
		if expected == nil && i == len(prepared.occurrences)-1 {
			expected = fx.Expected
		}
		if diff := compareCorpusExpectation(expected, res, prepared.store, prepared.sessionID, occ); diff != "" {
			result := failCorpus(fx, fmt.Sprintf("occurrence %d: %s", i, diff))
			result.Actual = actual
			return result
		}
		if i == len(prepared.occurrences)-1 && occ.Expected != nil {
			if diff := compareCorpusExpectation(fx.Expected, res, prepared.store, prepared.sessionID, occ); diff != "" {
				result := failCorpus(fx, diff)
				result.Actual = actual
				return result
			}
		}
	}
	result := passCorpus(fx)
	result.Actual = actual
	return result
}

func reportCorpusResult(res *PipelineResult, store *CounterStore, session string) map[string]any {
	applied := []string{}
	for _, entry := range res.Trace.Entries {
		applied = append(applied, entry.Rule+":"+string(entry.Outcome))
	}
	actions := []string{}
	for _, action := range res.AppliedOnFire {
		actions = append(actions, string(action))
	}
	events := []map[string]any{}
	for _, event := range res.PublishedEvents {
		events = append(events, map[string]any{"rule": event.Rule, "anchor": event.Anchor, "effect": string(event.Effect)})
	}
	skipped := res.SkippedTransforms
	if skipped == nil {
		skipped = []oarcore.SkippedTransform{}
	}
	counters := store.Report(session)
	for _, row := range counters {
		if _, ok := row["fire_count"]; !ok {
			row["fire_count"] = 0
		}
		if _, ok := row["breaker_count"]; !ok {
			row["breaker_count"] = 0
		}
	}
	actual := map[string]any{"decision": "none", "applied": applied, "on_fire": actions, "events": events, "counters": counters, "transforms": reportTransforms(res.Transforms), "skipped_transforms": skipped}
	advisories := []map[string]any{}
	if d := res.Decision; d != nil {
		actual["decision"] = string(d.Effect)
		actual["code"] = d.Code
		actual["rule"] = d.Rule
		actual["copy"] = d.Copy
		for _, item := range d.Advisories {
			advisories = append(advisories, map[string]any{"code": item.Code, "rule": item.Rule, "copy": item.Copy})
		}
	}
	actual["advisories"] = advisories
	if res.ContentSet {
		actual["content"] = res.Content
	}
	return actual
}

func corpusUnknownInputSkip(fx corpusFixture) (CorpusOutcome, bool) {
	known := map[string]bool{
		"capability": true, "anchor": true, "facts": true,
		"session_id": true, "recent_activity": true,
		"content": true, "config": true, "detector_facts": true,
		"occurrences": true, "tool_call": true,
	}
	var unknownKeys []string
	for k := range fx.RawInput {
		if !known[k] {
			unknownKeys = append(unknownKeys, k)
		}
	}
	if len(unknownKeys) == 0 {
		return CorpusOutcome{}, false
	}
	sort.Strings(unknownKeys)
	return skipCorpus(fx, "input keys this runner does not carry: "+strings.Join(unknownKeys, ", ")), true
}

func parseCorpusRules(fx corpusFixture, runner *ConformanceRunner) ([]*Rule, CorpusOutcome, bool) {
	rules := make([]*Rule, 0, len(fx.Rules))
	for _, raw := range fx.Rules {
		r, perr := runner.parseRuleJSON(raw)
		if perr != nil {
			return nil, corpusExpectedError(fx, perr, "load").outcome, false
		}
		rules = append(rules, r)
	}
	return rules, CorpusOutcome{}, true
}

func validateCorpusRuleSet(fx corpusFixture, rs *RuleSet) (CorpusOutcome, bool) {
	checks := []struct {
		phase string
		err   error
	}{
		{"identities", RejectDuplicateIdentities(rs)},
		{"overrides", ResolveOverrides(rs)},
		{"counters", RequireResolvableCounterReads(rs)},
		{"on_error", RejectUnknownErrorSubstitutes(rs)},
	}
	for _, check := range checks {
		if check.err == nil {
			continue
		}
		return corpusExpectedError(fx, check.err, check.phase).outcome, true
	}
	return CorpusOutcome{}, false
}

func applyCorpusOperatorConfig(fx corpusFixture, rs *RuleSet) (*RuleSet, CorpusOutcome, bool) {
	if len(fx.Input.Config) == 0 {
		return rs, CorpusOutcome{}, false
	}
	configured, err := ConfiguredRuleSet(fx.Input.Config, rs)
	if err != nil {
		result := corpusExpectedError(fx, err, "config")
		return nil, result.outcome, true
	}
	return configured, CorpusOutcome{}, false
}

func corpusOccurrences(fx corpusFixture) []corpusOcc {
	if len(fx.Input.Occurrences) > 0 {
		return fx.Input.Occurrences
	}
	return []corpusOcc{{
		ToolCall:       fx.Input.ToolCall,
		DetectorFacts:  fx.Input.DetectorFacts,
		Anchor:         fx.Input.Anchor,
		Facts:          fx.Input.Facts,
		RecentActivity: fx.Input.RecentActivity,
		Content:        fx.Input.Content,
		Expected:       fx.Expected,
	}}
}

func corpusExpectedError(fx corpusFixture, err error, phase string) corpusPrepResult {
	outcome := passCorpus(fx)
	if !matchExpectedError(fx.Expected, err) {
		outcome = failCorpus(fx, phase+": "+err.Error())
	}
	outcome.Actual = map[string]any{"error": err.Error()}
	return corpusDone(outcome)
}

func corpusDone(outcome CorpusOutcome) corpusPrepResult {
	return corpusPrepResult{outcome: outcome, done: true}
}

func skipCorpus(fx corpusFixture, reason string) CorpusOutcome {
	return CorpusOutcome{ID: fx.ID, Status: "skip", Reason: reason}
}

func failCorpus(fx corpusFixture, reason string) CorpusOutcome {
	return CorpusOutcome{ID: fx.ID, Status: "fail", Reason: reason}
}

func passCorpus(fx corpusFixture) CorpusOutcome {
	return CorpusOutcome{ID: fx.ID, Status: "pass"}
}

func parseFixtureCapability(raw json.RawMessage) (*CapabilityDocument, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("[OAR-CONF-19] missing input.capability")
	}
	var asYAML any
	if err := json.Unmarshal(raw, &asYAML); err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(asYAML)
	if err != nil {
		return nil, err
	}
	return ParseCapabilityDocumentShape(encoded)
}

func corpusReservedDetectors() *DetectorRegistry {
	reg := NewDetectorRegistry()
	reg.Register(corpusDetector{name: "noop"})
	reg.Register(corpusDetector{name: "error", fails: true})
	reg.Register(corpusDetector{name: "fixture"})
	return reg
}

type corpusDetector struct {
	name  string
	fails bool
}

func (d corpusDetector) Name() string { return d.name }

func (d corpusDetector) Inspect(*GuardContext) ([]Finding, error) {
	if d.fails {
		return nil, fmt.Errorf("detector://error always fails")
	}
	return nil, nil
}

type fixtureFactsDetector struct {
	facts map[string]any
}

func (d fixtureFactsDetector) Name() string { return "fixture" }

func (d fixtureFactsDetector) Inspect(gc *GuardContext) ([]Finding, error) {
	if gc == nil {
		return nil, nil
	}
	findings := make([]Finding, 0, len(d.facts))
	for k, v := range d.facts {
		findings = append(findings, Finding{Fact: k, Value: v})
	}
	return findings, nil
}
