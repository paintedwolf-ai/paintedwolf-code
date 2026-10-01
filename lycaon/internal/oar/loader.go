package oar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// oarFields is the Open Agent Rules 1.0 document, as it appears on disk.
// Unknown keys that are not `x-` extensions fail load ([OAR-DOC-27]).
type oarFields struct {
	OAR         string    `yaml:"oar,omitempty" json:"oar,omitempty"`
	ID          string    `yaml:"id,omitempty" json:"id,omitempty"`
	Namespace   string    `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Kind        string    `yaml:"kind,omitempty" json:"kind,omitempty"`
	Anchor      string    `yaml:"anchor,omitempty" json:"anchor,omitempty"`
	Selector    Selector  `yaml:"selector,omitempty" json:"selector,omitempty"`
	Requires    *Requires `yaml:"requires,omitempty" json:"requires,omitempty"`
	When        string    `yaml:"when,omitempty" json:"when,omitempty"`
	Flow        []string  `yaml:"flow,omitempty" json:"flow,omitempty"`
	Effect      string    `yaml:"effect,omitempty" json:"effect,omitempty"`
	Enforcement string    `yaml:"enforcement,omitempty" json:"enforcement,omitempty"`
	Mandatory   bool      `yaml:"mandatory,omitempty" json:"mandatory,omitempty"`
	OnError     string    `yaml:"on_error,omitempty" json:"on_error,omitempty"`
	OnFire      []string  `yaml:"on_fire,omitempty" json:"on_fire,omitempty"`
	Overrides   []string  `yaml:"overrides,omitempty" json:"overrides,omitempty"`
	// CounterScope names the declared string fact whose value keys this rule's
	// counters ([OAR-DOC-32]).
	CounterScope string        `yaml:"counter_scope,omitempty" json:"counter_scope,omitempty"`
	Detector     *DetectorRef  `yaml:"detector,omitempty" json:"detector,omitempty"`
	Status       string        `yaml:"status,omitempty" json:"status,omitempty"`
	References   References    `yaml:"references,omitempty" json:"references,omitempty"`
	Copy         *Copy         `yaml:"copy,omitempty" json:"copy,omitempty"`
	Transform    *rawTransform `yaml:"transform,omitempty" json:"transform,omitempty"`
}

type rawTransform struct {
	Action      string  `yaml:"action" json:"action"`
	Target      string  `yaml:"target" json:"target"`
	Replacement *string `yaml:"replacement,omitempty" json:"replacement,omitempty"`
}

// Loader validates and loads OAR rules.
type Loader struct {
	SchemaDir  string // directory containing oar.schema.json (+ refs); required for Validate
	schema     *jsonschema.Schema
	detectors  *DetectorRegistry // [OAR-OPS-12]: resolve detector.ref at load
	capability *CapabilityDocument
	specEnvErr error
	specEnv    *oarcore.Environment
	reachable  map[string]bool
	hostFacts  map[string]struct{}
}

// NewLoader constructs a loader. An empty schemaDir skips JSON Schema validation.
func NewLoader(schemaDir string) (*Loader, error) {
	l := &Loader{SchemaDir: schemaDir}
	if strings.TrimSpace(schemaDir) != "" {
		sch, err := compileOARSchema(schemaDir)
		if err != nil {
			return nil, err
		}
		l.schema = sch
	}
	l.refreshCapabilityCaches()
	return l, nil
}

// SetDetectors installs the load-time detector registry ([OAR-OPS-12]).
func (l *Loader) SetDetectors(r *DetectorRegistry) {
	if l != nil {
		l.detectors = r
	}
}

// Detectors returns the registry attached at load, or nil.
func (l *Loader) Detectors() *DetectorRegistry {
	if l == nil {
		return nil
	}
	return l.detectors
}

// SetCapability sets this loader's capability document.
func (l *Loader) SetCapability(p *CapabilityDocument) {
	if l == nil {
		return
	}
	l.capability = p
	l.refreshCapabilityCaches()
}

// ValidateCondition checks a condition against this loader's capability.
func (l *Loader) ValidateCondition(expr string) error {
	if l == nil {
		return fmt.Errorf("nil OAR loader")
	}
	return l.checkWhen(expr)
}

func (l *Loader) refreshCapabilityCaches() {
	capability := l.capabilityDoc()
	l.specEnv, l.specEnvErr = capabilityEnvironment(capability)
	l.reachable = specReachable(l.specEnv)
	l.hostFacts = capabilityHostFacts(capability)
}

func (l *Loader) checkWhen(expr string) error {
	if l.specEnvErr != nil {
		return l.specEnvErr
	}
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	return oarcore.CheckCondition(expr, l.specEnv, l.reachable)
}

func (l *Loader) capabilityDoc() *CapabilityDocument {
	if l != nil && l.capability != nil {
		return l.capability
	}
	return InstalledCapabilityDocument()
}

func compileOARSchema(schemaDir string) (*jsonschema.Schema, error) {
	// The rule schema is self-contained.
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"oar.schema.json"} {
		// schemas/oar/ is the vendored standard; ./task oar:vendor refreshes it.
		path := filepath.Join(schemaDir, "oar", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read vendored schema %s: %w "+
				"(run ./task oar:vendor with a verified OAR source bundle)", path, err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse schema %s: %w", path, err)
		}
		if err := compiler.AddResource(SchemaBaseURL+name, doc); err != nil {
			return nil, fmt.Errorf("add schema %s: %w", name, err)
		}
		if err := compiler.AddResource(name, doc); err != nil {
			return nil, fmt.Errorf("add schema file %s: %w", name, err)
		}
	}
	sch, err := compiler.Compile(SchemaBaseURL + "oar.schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile oar.schema.json: %w", err)
	}
	return sch, nil
}

// LoadDir compiles and links policy documents under dir as tier:builtin.
func (l *Loader) LoadDir(dir extpacks.Source) (*RuleSet, error) {
	return l.LoadDirTier(dir, TierBuiltin, dir.String())
}

// LoadEffectivePolicy loads policy units from every contributing extension
// pack — stock and installed — through the process catalog (resolving
// committed device state when none is active yet).
func (l *Loader) LoadEffectivePolicy() (*RuleSet, error) {
	entries, err := hintregistry.ListEffective()
	if err != nil {
		return nil, err
	}
	return l.loadEntries(entries, TierBuiltin, "stock")
}

// LoadEffectivePolicyWithCatalog loads policy units through an explicit catalog.
func (l *Loader) LoadEffectivePolicyWithCatalog(catalog *extpacks.EffectiveCatalog) (*RuleSet, error) {
	entries, err := hintregistry.ListEffectiveWithCatalog(catalog)
	if err != nil {
		return nil, err
	}
	return l.LoadEffectivePolicyEntries(entries)
}

// LoadEffectivePolicyEntries compiles a policy registry already captured from
// the effective catalog.
func (l *Loader) LoadEffectivePolicyEntries(entries []hintregistry.Entry) (*RuleSet, error) {
	return l.loadEntries(entries, TierBuiltin, "stock")
}

// LoadDirTier loads hint YAML under dir and stamps every rule with tier/source.
func (l *Loader) LoadDirTier(dir extpacks.Source, tier Tier, source string) (*RuleSet, error) {
	entries, err := hintregistry.List(dir)
	if err != nil {
		return nil, err
	}
	src := strings.TrimSpace(source)
	if src == "" {
		src = dir.String()
	}
	return l.loadEntries(entries, tier, src)
}

func (l *Loader) loadEntries(entries []hintregistry.Entry, tier Tier, source string) (*RuleSet, error) {
	rs, err := l.compileEntries(entries, tier, source)
	if err != nil {
		return nil, err
	}
	return linkRuleSet(rs)
}

func (l *Loader) compileEntries(entries []hintregistry.Entry, tier Tier, source string) (*RuleSet, error) {
	if l == nil {
		return nil, fmt.Errorf("oar loader not initialized")
	}
	if err := ValidateTier(tier); err != nil {
		return nil, err
	}
	src := strings.TrimSpace(source)
	var rules []*Rule
	var errs []string
	for _, ent := range entries {
		var node yaml.Node
		if err := yaml.Unmarshal(ent.Body, &node); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ent.Path, err))
			continue
		}
		if len(node.Content) == 0 {
			errs = append(errs, fmt.Sprintf("%s: empty policy body", ent.Path))
			continue
		}
		rule, skip, err := l.parseRuleNode(ent.Code, node.Content[0])
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s %s: %v", ent.Path, ent.Code, err))
			continue
		}
		if skip {
			continue
		}
		rule.Tier = tier
		rule.Source = ent.Path.String()
		if rule.Source == "" {
			rule.Source = src
		}
		rules = append(rules, rule)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("oar load:\n%s", strings.Join(errs, "\n"))
	}
	return NewRuleSet(rules), nil
}

func linkRuleSet(rs *RuleSet) (*RuleSet, error) {
	if err := RejectDuplicateIdentities(rs); err != nil {
		return nil, err
	}
	if err := ResolveOverrides(rs); err != nil {
		return nil, err
	}
	if err := RequireResolvableCounterReads(rs); err != nil {
		return nil, fmt.Errorf("oar load: %w", err)
	}
	if err := RejectUnknownErrorSubstitutes(rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func (l *Loader) parseRule(code string, raw map[string]any) (*Rule, bool, error) {
	b, err := yaml.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return nil, false, err
	}
	if len(node.Content) == 0 {
		return nil, false, fmt.Errorf("rule %q is empty", code)
	}
	return l.parseRuleDecoded(code, raw, node.Content[0])
}

func (l *Loader) parseRuleNode(code string, node *yaml.Node) (*Rule, bool, error) {
	if node == nil {
		return nil, false, fmt.Errorf("rule %q is empty", code)
	}
	var raw map[string]any
	if err := node.Decode(&raw); err != nil {
		return nil, false, err
	}
	return l.parseRuleDecoded(code, raw, node)
}

func (l *Loader) parseRuleDecoded(code string, raw map[string]any, node *yaml.Node) (*Rule, bool, error) {
	if _, present := raw["oar"]; !present {
		return nil, true, nil
	}
	if l.specEnvErr != nil {
		return nil, false, l.specEnvErr
	}
	compiled, err := oarcore.LoadRuleDocument(raw, l.specEnv)
	if err != nil {
		return nil, false, err
	}
	var entry guidance.HintEntry
	if err := node.Decode(&entry); err != nil {
		return nil, false, err
	}
	var fields oarFields
	if err := node.Decode(&fields); err != nil {
		return nil, false, err
	}
	rule := materialize(code, entry, fields)
	rule.document = compiled
	rule.Anchor = compiled.ResolvedAnchor
	if compiled.Transform != nil {
		t := compiled.Transform
		rule.Transform = &TransformSpec{Action: t.Action, Target: t.Target, Replacement: t.Replacement, HasReplacement: t.HasReplacement}
	}
	if rule.Detector != nil && (l.detectors == nil || l.detectors.Lookup(rule.Detector.Ref) == nil) {
		return nil, false, fmt.Errorf("[OAR-OPS-12] rule %s references unregistered detector %s", rule.Qualified(), rule.Detector.Ref)
	}
	if l.schema != nil {
		if err := l.validateSchema(rule.ID, raw); err != nil {
			return nil, false, err
		}
	}
	return rule, false, nil
}

func materialize(code string, entry guidance.HintEntry, oar oarFields) *Rule {
	r := &Rule{
		OAR:          strings.TrimSpace(oar.OAR),
		ID:           oar.ID,
		Kind:         Kind(strings.TrimSpace(oar.Kind)),
		Anchor:       strings.TrimSpace(oar.Anchor),
		Selector:     oar.Selector,
		When:         strings.TrimSpace(firstNonEmpty(oar.When, entry.When)),
		Flow:         append([]string(nil), oar.Flow...),
		Effect:       Effect(strings.TrimSpace(oar.Effect)),
		Enforcement:  firstNonEmpty(oar.Enforcement, "enforce"),
		Mandatory:    oar.Mandatory,
		OnError:      firstNonEmpty(oar.OnError, "fail_closed"),
		CounterScope: strings.TrimSpace(oar.CounterScope),
		Status:       firstNonEmpty(oar.Status, "stable"),
		Overrides:    append([]string(nil), oar.Overrides...),
		Emit:         entry.Emit,
		Audience:     append([]string(nil), entry.XAudience...),
		Scenarios:    append([]guidance.ScenarioEntry(nil), entry.Scenarios...),
		References:   oar.References,
		Detector:     oar.Detector,
		Namespace:    strings.TrimSpace(oar.Namespace),
	}
	if oar.Requires != nil {
		r.Requires = *oar.Requires
	}
	if oar.Copy != nil {
		r.Copy = *oar.Copy
	}
	// Copy fields feed the shared renderer fields.
	r.Title = firstNonEmpty(r.Copy.Title, entry.Title)
	r.What = firstNonEmpty(r.Copy.What, entry.What)
	r.Cause = firstNonEmpty(r.Copy.Cause, entry.Cause)
	r.Fix = firstNonEmpty(r.Copy.Fix, entry.Fix)
	for _, s := range oar.OnFire {
		if a, err := ParseOnFire(s); err == nil {
			r.OnFire = append(r.OnFire, a)
		}
	}

	return r
}

// validateSchema validates the authored document against oar.schema.json
// ([OAR-CONF-1]).
func (l *Loader) validateSchema(code string, raw map[string]any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("rule %q: %w", code, err)
	}
	if err := l.schema.Validate(doc); err != nil {
		return fmt.Errorf("rule %q: oar.schema.json: %w", code, err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ValidateDocument validates a raw JSON rule document against oar.schema.json.
// This is the closed-world check: any key the schema does not declare, and that
// is not an x- extension, is rejected here.
func (l *Loader) ValidateDocument(raw []byte) error {
	if l == nil || l.schema == nil {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("rule document: %w", err)
	}
	if err := l.schema.Validate(doc); err != nil {
		return fmt.Errorf("oar.schema.json: %w", err)
	}
	return nil
}
