package oar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// Conformance fixtures (section 10.2), run through the production engine. The
// published corpus uses core anchors and core or standard facts only
// ([OAR-CONF-17]); host fixtures seeded from pack scenarios use the same shape.

// ConformanceFixture is one fixture ([OAR-CONF-13], [OAR-CONF-14]).
type ConformanceFixture struct {
	ID     string            `json:"id"`
	Title  string            `json:"title,omitempty"`
	Covers []string          `json:"covers,omitempty"`
	Rule   json.RawMessage   `json:"rule"`
	Hints  []json.RawMessage `json:"hints,omitempty"`
	Input  FixtureInput      `json:"input"`
	// Expected holds the keys the fixture asserts ([OAR-CONF-15]).
	Expected map[string]json.RawMessage `json:"expected"`
}

// FixtureInput is the occurrence a fixture evaluates, after the earlier
// occurrences it lists in the same session.
type FixtureInput struct {
	Anchor      string              `json:"anchor"`
	Facts       map[string]any      `json:"facts"`
	RecentTools []string            `json:"recent_tools,omitempty"`
	SessionID   string              `json:"session_id,omitempty"`
	HostFacts   map[string]string   `json:"host_facts,omitempty"`
	Profile     json.RawMessage     `json:"profile,omitempty"`
	Occurrences []FixtureOccurrence `json:"occurrences,omitempty"`
}

// FixtureOccurrence is one earlier occurrence of a fixture's session.
type FixtureOccurrence struct {
	Anchor      string         `json:"anchor"`
	Facts       map[string]any `json:"facts"`
	RecentTools []string       `json:"recent_tools,omitempty"`
}

// expectedKeys are the only keys a fixture may assert; any other is rejected
// rather than passed silently ([OAR-CONF-15]). copy asserts the rendered copy
// a decision carries ([OAR-COPY-7]); the published corpus relies on it.
var expectedKeys = []string{"decision", "code", "on_fire", "applied", "counters", "copy", "error"}

// CorpusOutcome is one fixture's verdict.
type CorpusOutcome struct {
	ID     string         `json:"id"`
	Status string         `json:"status"` // pass | fail | skip
	Reason string         `json:"reason,omitempty"`
	Actual map[string]any `json:"actual,omitempty"`
}

// ConformanceRunner evaluates fixtures through the production engine.
type ConformanceRunner struct {
	loader *Loader
	// host, when set, is the engine pack fixtures run on; otherwise fixtures
	// run on the reference engine the published corpus assumes.
	host *EngineDeclaration
}

// NewConformanceRunner builds a runner for the published corpus. schemaDir
// may be empty to skip JSON Schema validation.
func NewConformanceRunner(schemaDir string) (*ConformanceRunner, error) {
	l, err := NewLoader(schemaDir)
	if err != nil {
		return nil, err
	}
	return &ConformanceRunner{loader: l}, nil
}

// NewHostConformanceRunner builds a runner whose fixtures run on host: its
// anchors, host facts, profiles and detectors, plus the corpus's reserved
// detectors.
func NewHostConformanceRunner(schemaDir string, host *EngineDeclaration) (*ConformanceRunner, error) {
	if host == nil {
		return nil, errors.New("host conformance runner requires an engine declaration")
	}
	cr, err := NewConformanceRunner(schemaDir)
	if err != nil {
		return nil, err
	}
	cr.host = host
	return cr, nil
}

// LoadFixturesDir loads the *.json fixtures in dir, skipping the corpus
// manifest and exemption list.
func LoadFixturesDir(dir string) ([]ConformanceFixture, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ConformanceFixture
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "manifest.json" || name == "exempt.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 — fixture directory supplied by the caller.
		if err != nil {
			return nil, err
		}
		fx, err := decodeFixture(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if fx.ID == "" {
			fx.ID = strings.TrimSuffix(name, ".json")
		}
		out = append(out, fx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// decodeFixture reads one fixture. Fact numbers keep their full range: a
// literal beyond a double's range is the non-finite value a fixture uses to
// reach a provider failure ([OAR-FACT-17]).
func decodeFixture(raw []byte) (ConformanceFixture, error) {
	var fx ConformanceFixture
	if err := checkFixtureShape(raw); err != nil {
		return fx, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fx); err != nil {
		return fx, err
	}
	for name, value := range fx.Input.Facts {
		fx.Input.Facts[name] = fixtureNumbers(value)
	}
	for _, occ := range fx.Input.Occurrences {
		for name, value := range occ.Facts {
			occ.Facts[name] = fixtureNumbers(value)
		}
	}
	return fx, nil
}

// The closed fixture vocabulary ([OAR-CONF-13], [OAR-CONF-15]).
var (
	fixtureMembers    = []string{"id", "title", "covers", "rule", "hints", "input", "expected"}
	inputMembers      = []string{"anchor", "facts", "recent_tools", "session_id", "host_facts", "profile", "occurrences"}
	occurrenceMembers = []string{"anchor", "facts", "recent_tools"}
)

// checkFixtureShape refuses a fixture carrying an undefined member, one
// missing input or expected, or one with neither rules nor hints.
func checkFixtureShape(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	for key := range top {
		if !slices.Contains(fixtureMembers, key) {
			return fmt.Errorf("[OAR-CONF-13] fixture carries undefined member %q", key)
		}
	}
	for _, key := range []string{"input", "expected"} {
		if _, ok := top[key]; !ok {
			return fmt.Errorf("[OAR-CONF-13] fixture is missing %q", key)
		}
	}
	_, hasRule := top["rule"]
	_, hasHints := top["hints"]
	if !hasRule && !hasHints {
		return fmt.Errorf("[OAR-CONF-13] fixture carries neither %q nor %q", "rule", "hints")
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(top["input"], &input); err != nil {
		return fmt.Errorf("[OAR-CONF-13] fixture input: %w", err)
	}
	for key := range input {
		if !slices.Contains(inputMembers, key) {
			return fmt.Errorf("[OAR-CONF-13] fixture input carries undefined member %q", key)
		}
	}
	var shape struct {
		Facts       map[string]json.RawMessage   `json:"facts"`
		SessionID   string                       `json:"session_id"`
		Occurrences []map[string]json.RawMessage `json:"occurrences"`
	}
	if err := json.Unmarshal(top["input"], &shape); err != nil {
		return fmt.Errorf("[OAR-CONF-13] fixture input: %w", err)
	}
	if len(shape.Occurrences) > 0 && shape.SessionID == "" {
		return fmt.Errorf("[OAR-CONF-13] fixture lists occurrences without a session_id")
	}
	factSets := []map[string]json.RawMessage{shape.Facts}
	for _, occ := range shape.Occurrences {
		for key := range occ {
			if !slices.Contains(occurrenceMembers, key) {
				return fmt.Errorf("[OAR-CONF-13] fixture occurrence carries undefined member %q", key)
			}
		}
		var facts map[string]json.RawMessage
		if raw, ok := occ["facts"]; ok {
			if err := json.Unmarshal(raw, &facts); err != nil {
				return fmt.Errorf("[OAR-CONF-13] fixture occurrence facts: %w", err)
			}
		}
		factSets = append(factSets, facts)
	}
	for _, facts := range factSets {
		for name := range facts {
			if oarcore.IsEngineMaintained(name) {
				return fmt.Errorf("[OAR-FIRE-10] fixture supplies %s, which the engine maintains", name)
			}
		}
	}
	return nil
}

func fixtureNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return t.String()
		}
		return f
	case map[string]any:
		for k, e := range t {
			t[k] = fixtureNumbers(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = fixtureNumbers(e)
		}
		return t
	}
	return v
}

// RunCorpusDir evaluates every fixture in dir.
func RunCorpusDir(ctx context.Context, dir string) ([]CorpusOutcome, error) {
	fixtures, err := LoadFixturesDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]CorpusOutcome, 0, len(fixtures))
	for _, fx := range fixtures {
		runner, err := NewConformanceRunner("")
		if err != nil {
			return nil, err
		}
		actual, err := runner.run(ctx, fx)
		outcome := CorpusOutcome{ID: fx.ID, Status: "pass", Actual: actual}
		if err != nil {
			outcome.Status, outcome.Reason = "fail", err.Error()
		}
		out = append(out, outcome)
	}
	return out, nil
}

// RunFixture evaluates one fixture and reports the first disagreement.
func (cr *ConformanceRunner) RunFixture(ctx context.Context, fx ConformanceFixture) error {
	_, err := cr.run(ctx, fx)
	return err
}

func (cr *ConformanceRunner) run(ctx context.Context, fx ConformanceFixture) (map[string]any, error) {
	for key := range fx.Expected {
		if !slices.Contains(expectedKeys, key) {
			return nil, fmt.Errorf("[OAR-CONF-15] fixture %s asserts undefined expected key %q", fx.ID, key)
		}
	}
	wantErr, expectsErr := expectedString(fx.Expected, "error")
	actual := map[string]any{}
	pipeline, store, err := cr.prepare(fx)
	if err != nil {
		if expectsErr {
			if !strings.Contains(err.Error(), wantErr) {
				return map[string]any{"error": err.Error()}, fmt.Errorf("error %q does not contain %q", err.Error(), wantErr)
			}
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"error": err.Error()}, err
	}
	if expectsErr {
		return actual, fmt.Errorf("expected load error containing %q, the fixture loaded", wantErr)
	}
	session := fx.Input.SessionID
	if session == "" {
		session = "fixture"
	}
	occurrences := append(append([]FixtureOccurrence(nil), fx.Input.Occurrences...),
		FixtureOccurrence{Anchor: fx.Input.Anchor, Facts: fx.Input.Facts, RecentTools: fx.Input.RecentTools})
	var res *PipelineResult
	for _, occ := range occurrences {
		if res, err = cr.evaluateOccurrence(ctx, pipeline, session, occ); err != nil {
			return map[string]any{"error": err.Error()}, err
		}
	}
	actual = corpusActual(res, store, session)
	return actual, compareExpected(fx.Expected, actual)
}

// evaluateOccurrence runs one occurrence of a fixture's session.
func (cr *ConformanceRunner) evaluateOccurrence(ctx context.Context, pipeline *GuardPipeline, session string, occ FixtureOccurrence) (*PipelineResult, error) {
	gc := factsToContext(occ.Facts)
	gc.Session.SessionID = session
	gc.Session.RecentToolNames = occ.RecentTools
	anchor := strings.TrimSpace(occ.Anchor)
	if anchor == "" {
		return nil, fmt.Errorf("fixture occurrence declares no anchor")
	}
	local, err := cr.loader.declaration().ResolveAnchor(anchor)
	if err != nil {
		local = anchor
	}
	pipeline.EnableAnchor(local)
	return pipeline.EvaluateBlock(ctx, local, gc)
}

// prepare builds the fixture's environment and loads its rules and hints.
func (cr *ConformanceRunner) prepare(fx ConformanceFixture) (*GuardPipeline, *CounterStore, error) {
	decl, err := fixtureDeclaration(cr.host, fx.Input)
	if err != nil {
		return nil, nil, err
	}
	cr.loader.SetDeclaration(decl)
	detectors := corpusReservedDetectors()
	cr.loader.SetDetectors(detectors)
	hints := make([]*oarcore.Hint, 0, len(fx.Hints))
	for _, raw := range fx.Hints {
		hint, err := cr.loader.LoadHintJSON(raw)
		if err != nil {
			return nil, nil, err
		}
		hints = append(hints, hint)
	}
	docs, err := fixtureRuleDocuments(fx.Rule)
	if err != nil {
		return nil, nil, err
	}
	rules := make([]*Rule, 0, len(docs))
	for _, doc := range docs {
		r, err := cr.loader.LoadRuleJSON(doc)
		if err != nil {
			return nil, nil, err
		}
		rules = append(rules, r)
	}
	rs := NewRuleSet(rules)
	rs.hints = hints
	if _, err := linkRuleSet(rs); err != nil {
		return nil, nil, err
	}
	store := NewCounterStore()
	p := NewGuardPipeline(rs, cr.loader, store)
	p.SetDetectors(detectors)
	p.SetEventPublisher(acceptPublisher{})
	return p, store, nil
}

// fixtureDeclaration is the engine a fixture runs on: the host's own when one
// is given, otherwise every core anchor mapped to itself with every standard
// profile claimed. Either way the fixture's profile map replaces the
// engine's ([OAR-CONF-19]), the reserved detectors are provided
// ([OAR-CONF-18]), and the host facts the fixture declares are added
// ([OAR-FACT-25]).
func fixtureDeclaration(host *EngineDeclaration, in FixtureInput) (*EngineDeclaration, error) {
	if host != nil {
		return hostFixtureDeclaration(host, in)
	}
	decl := &EngineDeclaration{
		Host:               "fixture",
		Profiles:           []string{ProfileFilesystem, ProfileContentSafety, ProfileMCP},
		Window:             16,
		Detectors:          []string{"detector://noop", "detector://error", "detector://fixture"},
		ExpressionNodesMax: 256,
		ProfileMap:         AnchorProfileMap{Version: SupportedSpecVersion, Core: map[string]string{}, Unsupported: []string{}},
	}
	for _, core := range coreAnchors {
		decl.ProfileMap.Core[core] = core
	}
	if len(in.Profile) > 0 {
		var m AnchorProfileMap
		if err := json.Unmarshal(in.Profile, &m); err != nil {
			return nil, fmt.Errorf("fixture profile map: %w", err)
		}
		if m.Unsupported == nil {
			m.Unsupported = []string{}
		}
		decl.ProfileMap = m
	}
	names := make([]string, 0, len(in.HostFacts))
	for name := range in.HostFacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if at := strings.Index(name, "."); at > 0 {
			decl.Host = name[:at]
		}
		decl.HostFacts = append(decl.HostFacts, HostFactDecl{Name: name, Type: in.HostFacts[name]})
	}
	if _, err := decl.Environment(); err != nil {
		return nil, err
	}
	return decl, nil
}

func hostFixtureDeclaration(host *EngineDeclaration, in FixtureInput) (*EngineDeclaration, error) {
	decl := *host
	decl.HostFacts = append([]HostFactDecl(nil), host.HostFacts...)
	decl.Detectors = append([]string(nil), host.Detectors...)
	for _, ref := range []string{"detector://noop", "detector://error", "detector://fixture"} {
		if !slices.Contains(decl.Detectors, ref) {
			decl.Detectors = append(decl.Detectors, ref)
		}
	}
	if len(in.Profile) > 0 {
		var m AnchorProfileMap
		if err := json.Unmarshal(in.Profile, &m); err != nil {
			return nil, fmt.Errorf("fixture profile map: %w", err)
		}
		if m.Unsupported == nil {
			m.Unsupported = []string{}
		}
		decl.ProfileMap = m
	}
	declared := map[string]string{}
	for _, f := range decl.HostFacts {
		declared[f.Name] = f.Type
	}
	names := make([]string, 0, len(in.HostFacts))
	for name := range in.HostFacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		typ := in.HostFacts[name]
		if have, ok := declared[name]; ok {
			if have != typ {
				return nil, fmt.Errorf("fixture declares %s as %s; this host declares %s", name, typ, have)
			}
			continue
		}
		decl.HostFacts = append(decl.HostFacts, HostFactDecl{Name: name, Type: typ})
	}
	if _, err := decl.Environment(); err != nil {
		return nil, err
	}
	return &decl, nil
}

func fixtureRuleDocuments(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	return []json.RawMessage{raw}, nil
}

// corpusActual projects a result into the expected keys. The trace and the
// counters name rules by identity ([OAR-CONF-15]).
func corpusActual(res *PipelineResult, store *CounterStore, session string) map[string]any {
	applied := []string{}
	for _, entry := range res.Trace.Entries {
		applied = append(applied, entry.Rule+":"+traceOutcome(entry.Outcome))
	}
	actual := map[string]any{"decision": "none", "applied": applied, "on_fire": []string{}}
	counters := map[string]any{}
	for rule, row := range store.Report(session) {
		values := map[string]any{}
		for k, v := range row {
			values[k] = v
		}
		counters[rule] = values
	}
	actual["counters"] = counters
	if d := res.Decision; d != nil {
		actual["decision"] = string(d.Effect)
		actual["code"] = d.Code
		var onFire []string
		for _, a := range d.OnFire {
			onFire = append(onFire, string(a))
		}
		if onFire == nil {
			onFire = []string{}
		}
		actual["on_fire"] = onFire
		if len(d.Copy) > 0 {
			actual["copy"] = d.Copy
		}
	}
	return actual
}

// traceOutcome spells a trace outcome as fixtures do ([OAR-CONF-15]).
func traceOutcome(o TraceOutcome) string {
	switch o {
	case TraceMonitoredFired, TraceMonitoredPassed, TraceMonitoredErrored:
		return "monitor"
	default:
		return string(o)
	}
}

func expectedString(expected map[string]json.RawMessage, key string) (string, bool) {
	raw, present := expected[key]
	if !present {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return string(raw), true
	}
	return s, true
}

// compareExpected checks each asserted key; counters compare only the
// values a fixture names.
func compareExpected(expected map[string]json.RawMessage, actual map[string]any) error {
	keys := make([]string, 0, len(expected))
	for k := range expected {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "error" {
			continue
		}
		var want any
		if err := json.Unmarshal(expected[key], &want); err != nil {
			return fmt.Errorf("expected %s: %w", key, err)
		}
		got := normalizeJSON(actual[key])
		if key == "counters" {
			if err := compareCounters(want, got); err != nil {
				return err
			}
			continue
		}
		if key == "copy" {
			if err := compareCopy(want, got, actual["decision"]); err != nil {
				return err
			}
			continue
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	return nil
}

// compareCopy checks the members a fixture names; null asserts the decision
// carries no copy.
func compareCopy(want, got, decision any) error {
	if decision == "none" {
		return fmt.Errorf("copy expected, the occurrence produced no decision")
	}
	if want == nil {
		if got != nil {
			return fmt.Errorf("copy = %v, want none", got)
		}
		return nil
	}
	wantMembers, _ := want.(map[string]any)
	gotMembers, _ := got.(map[string]any)
	for member, w := range wantMembers {
		if g := gotMembers[member]; g != w {
			return fmt.Errorf("copy %s = %v, want %v", member, g, w)
		}
	}
	return nil
}

func compareCounters(want, got any) error {
	wantRules, _ := want.(map[string]any)
	gotRules, _ := got.(map[string]any)
	for rule, values := range wantRules {
		wantValues, _ := values.(map[string]any)
		gotValues, _ := gotRules[rule].(map[string]any)
		for name, v := range wantValues {
			g, present := gotValues[name]
			if !present {
				g = float64(0)
			}
			if !reflect.DeepEqual(v, g) {
				return fmt.Errorf("counters %s.%s = %v, want %v", rule, name, g, v)
			}
		}
	}
	return nil
}

// normalizeJSON round-trips a value through JSON so it compares with decoded
// expectations.
func normalizeJSON(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

// corpusReservedDetectors are the three references the corpus reserves
// ([OAR-CONF-18]). detector://fixture reports nothing of its own, so the
// detector facts a fixture supplies stay in place.
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

// SeedFixturesFromScenarios writes one fixture per listed rule: its own
// document, an occurrence at its anchor, and the decision it produces.
func SeedFixturesFromScenarios(policyDir, outDir string, codes []string) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	if strings.TrimSpace(policyDir) == "" {
		if err := anchorcatalog.InstallBundled(); err != nil {
			return fmt.Errorf("install catalog: %w", err)
		}
	} else {
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
	if strings.TrimSpace(policyDir) == "" {
		rs, err = l.LoadEffectivePolicy()
	} else {
		rs, err = l.LoadDir(extpacks.OnDisk(policyDir))
	}
	if err != nil {
		return err
	}
	for _, code := range codes {
		r, ok := rs.Get(code)
		if !ok || r.When == "" || r.raw == nil {
			continue
		}
		doc, err := json.Marshal(r.raw)
		if err != nil {
			return err
		}
		facts := map[string]any{"tool": firstTool(r), oarcopy.HostFactNamespace + ".session_posture": "spec"}
		if r.Anchor == AnchorToolRejected {
			facts[oarcopy.HostFactNamespace+".rejection_code"] = code
		}
		decision, err := json.Marshal(string(r.Effect))
		if err != nil {
			return err
		}
		ruleCode, err := json.Marshal(r.ID)
		if err != nil {
			return err
		}
		fx := ConformanceFixture{
			ID: code, Rule: doc,
			Input:    FixtureInput{Anchor: r.Anchor, Facts: facts},
			Expected: map[string]json.RawMessage{"decision": decision, "code": ruleCode},
		}
		raw, err := json.MarshalIndent(fx, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, code+".json"), append(raw, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func firstTool(r *Rule) string {
	if tools := r.Selector["tools"]; len(tools) > 0 {
		return tools[0]
	}
	return ""
}

// ErrRuleNotEligible marks a document the loader skipped as not an OAR rule.
var ErrRuleNotEligible = errors.New("not OAR-eligible")
