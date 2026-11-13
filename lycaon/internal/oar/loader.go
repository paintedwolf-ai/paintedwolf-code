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
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// oarFields are the document fields the production rule reads beyond what
// the core engine compiles.
type oarFields struct {
	Title        string       `yaml:"title,omitempty" json:"title,omitempty"`
	Detector     *DetectorRef `yaml:"detector,omitempty" json:"detector,omitempty"`
	References   References   `yaml:"references,omitempty" json:"references,omitempty"`
	CounterScope string       `yaml:"x-paintedwolf-counter-scope,omitempty" json:"x-paintedwolf-counter-scope,omitempty"`
	// Transform names the content a transform rule replaces and how
	// ([OAR-OPS-8]); the standard leaves its shape to the host.
	Transform *transformField `yaml:"x-paintedwolf-transform,omitempty" json:"x-paintedwolf-transform,omitempty"`
}

type transformField struct {
	Action      string  `yaml:"action"`
	Target      string  `yaml:"target"`
	Replacement *string `yaml:"replacement"`
}

// Loader validates and loads OAR rules.
type Loader struct {
	SchemaDir   string // directory containing oar.schema.json (+ refs); required for Validate
	schema      *jsonschema.Schema
	detectors   *DetectorRegistry // [OAR-OPS-10]: resolve detector.ref at load
	decl        *EngineDeclaration
	specEnvErr  error
	specEnv     *oarcore.Environment
	reachable   map[string]bool
	hostFacts   map[string]struct{}
	toolSchemas *toolschema.Config
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
	l.refreshEnvironment()
	return l, nil
}

// SetDetectors installs the load-time detector registry ([OAR-OPS-10]).
func (l *Loader) SetDetectors(r *DetectorRegistry) {
	if l != nil {
		l.detectors = r
	}
}

// SetToolSchemas installs the declared tool argument schemas checked at load.
func (l *Loader) SetToolSchemas(c *toolschema.Config) {
	if l != nil {
		l.toolSchemas = c
	}
}

// Detectors returns the registry attached at load, or nil.
func (l *Loader) Detectors() *DetectorRegistry {
	if l == nil {
		return nil
	}
	return l.detectors
}

// SetDeclaration sets the engine this loader compiles against.
func (l *Loader) SetDeclaration(p *EngineDeclaration) {
	if l == nil {
		return
	}
	l.decl = p
	l.refreshEnvironment()
}

// ValidateCondition checks a condition against this loader's environment.
func (l *Loader) ValidateCondition(expr string) error {
	if l == nil {
		return fmt.Errorf("nil OAR loader")
	}
	return l.checkWhen(expr)
}

func (l *Loader) refreshEnvironment() {
	decl := l.declaration()
	l.specEnv, l.specEnvErr = capabilityEnvironment(decl)
	l.reachable = specReachable(l.specEnv)
	l.hostFacts = capabilityHostFacts(decl)
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

func (l *Loader) declaration() *EngineDeclaration {
	if l != nil && l.decl != nil {
		return l.decl
	}
	return InstalledEngineDeclaration()
}

func compileOARSchema(schemaDir string) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"oar.schema.json", "selector.schema.json"} {
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
		if len(ent.NextSteps) > 0 {
			steps, err := DecodeNextSteps(ent.NextSteps)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s next-steps: %v", ent.Code, err))
				continue
			}
			rule.NextSteps = steps
		}
		if err := l.validateNextSteps(rule); err != nil {
			errs = append(errs, fmt.Sprintf("%s %s: %v", ent.Path, ent.Code, err))
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
	if _, present := raw["spec_version"]; !present {
		return nil, true, nil
	}
	if l.specEnvErr != nil {
		return nil, false, l.specEnvErr
	}
	compiled, hint, err := oarcore.LoadDocument(raw, l.specEnv)
	if err != nil {
		return nil, false, err
	}
	if l.schema != nil {
		if err := l.validateSchema(code, raw); err != nil {
			return nil, false, err
		}
	}
	// A hint is loaded and validated like any document and never evaluated
	// ([OAR-HINT-1]); the hint registry renders it.
	if hint != nil {
		return nil, true, nil
	}
	var entry guidance.HintEntry
	if err := node.Decode(&entry); err != nil {
		return nil, false, err
	}
	var fields oarFields
	if err := node.Decode(&fields); err != nil {
		return nil, false, err
	}
	rule := materialize(compiled, entry, fields)
	rule.raw = raw
	if scope := rule.CounterScope; scope != "" {
		decl, declared := compiled.FactDeclaration(scope)
		if !declared || decl.Type != oarcore.TypeString || !containsExact(rule.RequiresFacts, scope) {
			return nil, false, fmt.Errorf("rule %s: x-paintedwolf-counter-scope %s must be a string fact the rule requires", rule.Qualified(), scope)
		}
	}
	if rule.Detector != nil && (l.detectors == nil || l.detectors.Lookup(rule.Detector.Ref) == nil) {
		return nil, false, fmt.Errorf("[OAR-OPS-10] rule %s references unregistered detector %s", rule.Qualified(), rule.Detector.Ref)
	}
	return rule, false, nil
}

// materialize builds the production rule from the compiled document and the
// host's own extension fields.
func materialize(doc *oarcore.Rule, entry guidance.HintEntry, fields oarFields) *Rule {
	r := &Rule{
		document:      doc,
		SpecVersion:   SupportedSpecVersion,
		ID:            doc.ID,
		Namespace:     doc.Namespace,
		Kind:          Kind(doc.Kind),
		Anchor:        doc.ResolvedAnchor,
		Selector:      documentSelector(doc),
		Flow:          append([]string(nil), doc.Flow...),
		Effect:        Effect(doc.Effect),
		Enforcement:   doc.Enforcement,
		Mandatory:     doc.Mandatory,
		OnError:       doc.OnError,
		RequiresFacts: append([]string(nil), doc.RequiresFacts...),
		Severity:      doc.Severity,
		Emit:          entry.Emit,
		Audience:      append([]string(nil), entry.XAudience...),
		Scenarios:     append([]guidance.ScenarioEntry(nil), entry.Scenarios...),
		References:    fields.References,
		Detector:      fields.Detector,
		Title:         fields.Title,
	}
	r.CounterScope = strings.TrimSpace(fields.CounterScope)
	if t := fields.Transform; t != nil {
		r.Transform = &TransformSpec{Action: t.Action, Target: t.Target, HasReplacement: t.Replacement != nil}
		if t.Replacement != nil {
			r.Transform.Replacement = *t.Replacement
		}
	}
	if doc.When != nil {
		r.When = strings.TrimSpace(entry.When)
	}
	r.Copy = Copy{
		Title: fields.Title, What: doc.Copy["what"], Cause: doc.Copy["cause"], Why: doc.Copy["why"],
		Fix: doc.Copy["fix"], Instead: doc.Copy["instead"], Message: doc.Copy["message"],
	}
	for _, s := range doc.OnFire {
		if a, err := ParseOnFire(s); err == nil {
			r.OnFire = append(r.OnFire, a)
		}
	}
	return r
}

// documentSelector is the fact-keyed view of a document's selector: the core
// fact behind each core dimension and each ledger fact, which fact assembly
// produces before matching. Matching itself runs on the document.
func documentSelector(doc *oarcore.Rule) Selector {
	out := Selector{}
	for _, clause := range doc.Selector {
		switch clause.Dimension {
		case "tool", "tools":
			out["tool"] = append(out["tool"], clause.Values...)
		case "profiles":
			out["profile"] = append(out["profile"], clause.Values...)
		case "session_posture":
			out["session_posture"] = append(out["session_posture"], clause.Values...)
		case "ledger":
			for name, values := range clause.Ledger {
				out[name] = append(out[name], values...)
			}
		}
	}
	return out
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

// LoadRuleJSON compiles one rule document given as JSON. Unlike a pack
// member, a document without spec_version is refused, as is a hint.
func (l *Loader) LoadRuleJSON(raw json.RawMessage) (*Rule, error) {
	doc, err := decodeDocumentJSON(raw)
	if err != nil {
		return nil, err
	}
	if _, present := doc["spec_version"]; !present {
		_, _, err := oarcore.LoadDocument(doc, l.specEnv)
		if err == nil {
			err = fmt.Errorf("document carries no spec_version")
		}
		return nil, err
	}
	id, _ := doc["id"].(string)
	rule, skip, err := l.parseRule(id, doc)
	if err != nil {
		return nil, err
	}
	if skip {
		return nil, fmt.Errorf("[OAR-HINT-1] %s is a hint, not a rule", id)
	}
	return rule, nil
}

// LoadHintJSON validates one hint document given as JSON. Hints are loaded
// and never evaluated ([OAR-HINT-1]).
func (l *Loader) LoadHintJSON(raw json.RawMessage) error {
	doc, err := decodeDocumentJSON(raw)
	if err != nil {
		return err
	}
	if l.specEnvErr != nil {
		return l.specEnvErr
	}
	_, hint, err := oarcore.LoadDocument(doc, l.specEnv)
	if err != nil {
		return err
	}
	if hint == nil {
		return fmt.Errorf("[OAR-HINT-1] hints list carries a rule document")
	}
	if l.schema != nil {
		id, _ := doc["id"].(string)
		return l.validateSchema(id, doc)
	}
	return nil
}

func decodeDocumentJSON(raw json.RawMessage) (map[string]any, error) {
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("rule document: %w", err)
	}
	return normalizeJSONNumbers(doc).(map[string]any), nil
}

// normalizeJSONNumbers keeps integral JSON numbers as int so int-typed fields
// compare as they would from YAML.
func normalizeJSONNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeJSONNumbers(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalizeJSONNumbers(e)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		f, _ := t.Float64()
		return f
	}
	return v
}
