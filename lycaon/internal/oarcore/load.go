package oarcore

// The version gate, the closed field set, anchor resolution, fact
// negotiation, selector clauses, and a full type check of the condition and
// copy all run at load, before any occurrence is evaluated. Malformed
// documents, unknown identifiers, and type errors are load-time rejections and
// never route through `on_error`.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LoadError is a document, or a rule set, this engine refuses. Every rejection
// names the field, identifier, or fact at fault.
type LoadError struct{ Message string }

func (e *LoadError) Error() string { return e.Message }

func loadErr(format string, args ...any) error {
	return &LoadError{Message: fmt.Sprintf(format, args...)}
}

var (
	idRE           = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	anchorRE       = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	factNameRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	qualifiedRefRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*/[A-Z][A-Z0-9_]*$`)
	extensionRE    = regexp.MustCompile(`^x-[a-z0-9]+(-[a-z0-9]+)*$`)
	ruleDetectorRE = regexp.MustCompile(`^detector://[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// ruleFields is the closed field set of a rule document ([OAR-DOC-20]).
var ruleFields = []string{
	"spec_version", "id", "title", "kind", "on", "selector", "effect", "severity",
	"when", "flow", "enforcement", "mandatory", "on_error", "on_fire", "stream",
	"references", "detector", "tools", "namespace", "requires_facts", "profile",
	"emit", "category", "context_schema", "scenarios", "evidence",
	"what", "cause", "why", "fix", "instead", "message",
}

// hintFields is the closed field set of a hint document ([OAR-HINT-1]).
var hintFields = []string{
	"spec_version", "id", "title", "kind", "namespace", "severity", "references", "emit",
	"what", "cause", "why", "fix", "instead", "message",
}

// copyMembers are the presentation strings a document may carry
// ([OAR-DOC-18]); a rule's may bind facts (Appendix B), a hint's is static.
var copyMembers = []string{"what", "cause", "why", "fix", "instead", "message"}

var (
	severities = []string{"block", "warn", "info"}
	streams    = []string{"buffered", "chunk"}
	authoring  = []string{"portable", "host"}
)

// SelectorClause is one selector clause over a dimension of the closed
// selector vocabulary ([OAR-SEL-7]). A null clause matches an occurrence
// where the dimension is unbound.
type SelectorClause struct {
	Dimension string
	Values    []string
	Null      bool
	// Ledger holds the typed ledger facts a ledger clause names, each with
	// the values it accepts.
	Ledger map[string][]string
}

// Rule is one loaded rule document.
type Rule struct {
	environment *Environment
	ID          string
	Namespace   string
	// Qualified is the identity of a rule: <namespace>/<id>, or the bare id when
	// namespace is absent ([OAR-DOC-22]).
	Qualified string
	Kind      string
	Effect    string
	// ResolvedAnchor is the local anchor a core anchor resolves to through the
	// profile map ([OAR-PROF-3]).
	ResolvedAnchor string
	Selector       []SelectorClause
	When           *node
	Flow           []string
	Enforcement    string
	Mandatory      bool
	OnError        string
	OnFire         []string
	Stream         string
	Severity       string
	RequiresFacts  []string
	Copy           map[string]string
	// Refs is every fact and function name the condition, selector and copy
	// reference.
	Refs map[string]bool
}

// Hint is one loaded hint document ([OAR-HINT-1]): static copy keyed by the
// code a host raises. It is never selected or evaluated.
type Hint struct {
	ID        string
	Namespace string
	Title     string
	Severity  string
	Emit      string
	Copy      map[string]string
}

// ResolveRef resolves a bare or qualified rule reference within a referring
// namespace ([OAR-DOC-22]).
func ResolveRef(ref, namespace string) string {
	if strings.Contains(ref, "/") || namespace == "" {
		return ref
	}
	return namespace + "/" + ref
}

func stringList(v any, field string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		if typed, ok := v.([]string); ok {
			return append([]string(nil), typed...), nil
		}
		return nil, loadErr("field %q must be a list of strings", field)
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		s, ok := entry.(string)
		if !ok {
			return nil, loadErr("field %q must be a list of strings", field)
		}
		out = append(out, s)
	}
	return out, nil
}

// LoadDocument loads a rule or a hint document. Exactly one of the results is
// non-nil when err is nil.
func LoadDocument(doc any, env *Environment) (*Rule, *Hint, error) {
	obj, ok := asObject(doc)
	if !ok {
		return nil, nil, loadErr("[OAR-DOC-1] a rule set member is not a document: expected a JSON object")
	}
	// [OAR-CONF-2] The version gate runs before any other processing.
	if err := checkVersion(obj); err != nil {
		return nil, nil, err
	}
	if kind, _ := obj["kind"].(string); kind == "hint" {
		hint, err := loadHint(obj)
		return nil, hint, err
	}
	rule, err := loadRule(obj, env)
	return rule, nil, err
}

// LoadRuleDocument loads one rule document against a declared environment. A
// hint document is refused here: it is not a rule.
func LoadRuleDocument(doc any, env *Environment) (*Rule, error) {
	rule, hint, err := LoadDocument(doc, env)
	if err != nil {
		return nil, err
	}
	if hint != nil {
		return nil, loadErr("[OAR-DOC-2] document %s has kind hint and is not a rule", hint.ID)
	}
	return rule, nil
}

func loadHint(obj map[string]any) (*Hint, error) {
	if err := checkClosedWorld(obj, hintFields, "hint"); err != nil {
		return nil, err
	}
	id, ok := obj["id"].(string)
	if !ok || !idRE.MatchString(id) {
		return nil, loadErr("[OAR-HINT-2] field \"id\" does not match ^[A-Z][A-Z0-9_]*$: %v", obj["id"])
	}
	hint := &Hint{ID: id, Copy: map[string]string{}}
	if err := loadIdentity(obj, &hint.Namespace); err != nil {
		return nil, err
	}
	var err error
	if hint.Title, err = optionalString(obj, "title"); err != nil {
		return nil, err
	}
	if hint.Emit, err = optionalString(obj, "emit"); err != nil {
		return nil, err
	}
	if hint.Severity, err = optionalEnum(obj, "severity", severities); err != nil {
		return nil, err
	}
	if err := loadReferences(obj); err != nil {
		return nil, err
	}
	for _, member := range copyMembers {
		raw, present := obj[member]
		if !present {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return nil, loadErr("field %q must be a string", member)
		}
		// [OAR-HINT-3] A hint's copy is static: it carries no bindings.
		if strings.Contains(s, "{{") || strings.Contains(s, "{%") {
			return nil, loadErr("[OAR-HINT-3] hint %s field %s carries a binding; a hint's copy is static", id, member)
		}
		hint.Copy[member] = s
	}
	// [OAR-HINT-2] A hint that publishes no copy is not a hint.
	if len(hint.Copy) == 0 {
		return nil, loadErr("[OAR-HINT-2] hint %s carries no copy field", id)
	}
	return hint, nil
}

func loadRule(obj map[string]any, env *Environment) (*Rule, error) {
	if err := checkClosedWorld(obj, ruleFields, "rule"); err != nil {
		return nil, err
	}
	// [OAR-DOC-2] The obligatory fields.
	for _, required := range []string{"id", "kind", "on", "selector", "effect"} {
		if _, present := obj[required]; !present {
			return nil, loadErr("[OAR-DOC-2] rule document is missing required field %q", required)
		}
	}
	id, ok := obj["id"].(string)
	if !ok || !idRE.MatchString(id) {
		return nil, loadErr("[OAR-DOC-4] field \"id\" does not match ^[A-Z][A-Z0-9_]*$: %v", obj["id"])
	}
	rule := &Rule{ID: id, Copy: map[string]string{}, Refs: map[string]bool{}, environment: env}
	if err := loadIdentity(obj, &rule.Namespace); err != nil {
		return nil, err
	}
	rule.Qualified = ResolveRef(id, rule.Namespace)

	kind, ok := obj["kind"].(string)
	if !ok || !contains(kinds, kind) {
		return nil, loadErr("[OAR-DOC-6] field \"kind\" is %v, not one of %s", obj["kind"], strings.Join(append(append([]string(nil), kinds...), "hint"), ", "))
	}
	rule.Kind = kind
	effect, ok := obj["effect"].(string)
	if !ok || !contains(effects, effect) {
		return nil, loadErr("[OAR-DOC-8] field \"effect\" is %v, not one of %s", obj["effect"], strings.Join(effects, ", "))
	}
	rule.Effect = effect

	if err := loadScalarFields(obj, rule, env); err != nil {
		return nil, err
	}
	if err := loadReferences(obj); err != nil {
		return nil, err
	}
	if err := loadInformational(obj); err != nil {
		return nil, err
	}
	if err := loadDetectorField(obj, rule, env); err != nil {
		return nil, err
	}
	if err := resolveAnchor(obj, rule, env); err != nil {
		return nil, err
	}
	reachable, err := loadRequiresFacts(obj, rule, env)
	if err != nil {
		return nil, err
	}
	if err := loadSelector(obj, rule, env); err != nil {
		return nil, err
	}
	if err := loadWhen(obj, rule, env); err != nil {
		return nil, err
	}
	if err := loadCopy(obj, rule, env, reachable); err != nil {
		return nil, err
	}
	if err := loadFlow(obj, rule, env); err != nil {
		return nil, err
	}
	return rule, nil
}

// checkVersion is the version gate ([OAR-DOC-3], [OAR-CONF-2]): a bad version
// wins over every other fault in the document.
func checkVersion(obj map[string]any) error {
	raw, present := obj["spec_version"]
	if !present {
		return loadErr("[OAR-DOC-2] document is missing required field \"spec_version\"")
	}
	version, ok := raw.(string)
	if !ok || !versionRE.MatchString(version) {
		return loadErr("[OAR-DOC-3] field \"spec_version\" must be a version string of the form <major>.<minor>")
	}
	parts := strings.SplitN(version, ".", 2)
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major != oarMajor {
		return loadErr("[OAR-CONF-2] spec_version %s names a major version this engine does not implement (%d.%d)", version, oarMajor, oarMinor)
	}
	if minor > oarMinor {
		// [OAR-DOC-3] An unsupported version is refused, never downgraded.
		return loadErr("[OAR-DOC-3] spec_version %s names a minor version above this engine's own (%d.%d)", version, oarMajor, oarMinor)
	}
	return nil
}

// checkClosedWorld refuses a field the document model does not define; an
// x- extension is ignored ([OAR-DOC-20]).
func checkClosedWorld(obj map[string]any, defined []string, what string) error {
	names := make([]string, 0, len(obj))
	for key := range obj {
		names = append(names, key)
	}
	// Sorted so the rejection names the same undefined field on every run.
	sort.Strings(names)
	for _, key := range names {
		if contains(defined, key) || extensionRE.MatchString(key) {
			continue
		}
		return loadErr("[OAR-DOC-20] %s document carries undefined field %q", what, key)
	}
	return nil
}

func loadIdentity(obj map[string]any, namespace *string) error {
	raw, present := obj["namespace"]
	if !present {
		return nil
	}
	ns, ok := raw.(string)
	if !ok || !namespaceRE.MatchString(ns) {
		return loadErr("[OAR-DOC-21] field \"namespace\" is not a publisher scope: %v", raw)
	}
	*namespace = ns
	return nil
}

func optionalString(obj map[string]any, field string) (string, error) {
	raw, present := obj[field]
	if !present {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", loadErr("field %q must be a string", field)
	}
	return s, nil
}

func optionalEnum(obj map[string]any, field string, allowed []string) (string, error) {
	s, err := optionalString(obj, field)
	if err != nil || s == "" {
		if _, present := obj[field]; present && s == "" && err == nil {
			return "", loadErr("field %q must be one of %s", field, strings.Join(allowed, ", "))
		}
		return s, err
	}
	if !contains(allowed, s) {
		return "", loadErr("field %q must be one of %s", field, strings.Join(allowed, ", "))
	}
	return s, nil
}

// loadScalarFields reads the fields whose values the engine and host act on.
func loadScalarFields(obj map[string]any, rule *Rule, env *Environment) error {
	var err error
	if rule.Enforcement, err = optionalEnum(obj, "enforcement", enforcements); err != nil {
		return err
	}
	if rule.Enforcement == "" {
		rule.Enforcement = "enforce"
	}
	if raw, present := obj["mandatory"]; present {
		v, ok := raw.(bool)
		if !ok {
			return loadErr("[OAR-DOC-12] field \"mandatory\" must be a boolean")
		}
		rule.Mandatory = v
	}
	rule.OnError = "fail_closed"
	if raw, present := obj["on_error"]; present {
		v, ok := raw.(string)
		if !ok || (v != "fail_closed" && v != "fail_open" && !idRE.MatchString(v) && !qualifiedRefRE.MatchString(v)) {
			return loadErr("[OAR-DOC-13] field \"on_error\" must be fail_closed, fail_open, or a rule identifier, got %v", raw)
		}
		rule.OnError = v
	}
	if raw, present := obj["on_fire"]; present {
		actions, err := stringList(raw, "on_fire")
		if err != nil {
			return err
		}
		for _, action := range actions {
			if !contains(onFireActions, action) {
				return loadErr("[OAR-FIRE-1] field \"on_fire\" names undefined side-effect %q", action)
			}
		}
		rule.OnFire = actions
	}
	if rule.Stream, err = optionalEnum(obj, "stream", streams); err != nil {
		return err
	}
	if rule.Stream == "" {
		rule.Stream = "buffered"
	}
	// [OAR-OPS-6] An engine that does not stream refuses a chunked rule rather
	// than buffering it silently.
	if rule.Stream == "chunk" && !env.SupportsChunk {
		return loadErr("[OAR-OPS-6] rule %s requests stream chunk, which this engine does not support", rule.Qualified)
	}
	// [OAR-OPS-8] An engine without content mutation refuses a transform rule.
	if rule.Effect == "transform" && !env.SupportsTransform {
		return loadErr("[OAR-OPS-8] rule %s has effect transform, which this engine does not implement", rule.Qualified)
	}
	if rule.Severity, err = optionalEnum(obj, "severity", severities); err != nil {
		return err
	}
	if _, err := optionalEnum(obj, "profile", authoring); err != nil {
		return err
	}
	if _, err := optionalString(obj, "title"); err != nil {
		return err
	}
	return nil
}

// loadReferences validates the taxonomy tags, which never affect a decision
// ([OAR-DOC-16]).
func loadReferences(obj map[string]any) error {
	raw, present := obj["references"]
	if !present {
		return nil
	}
	member, ok := asObject(raw)
	if !ok {
		return loadErr("field \"references\" must be an object")
	}
	for key, value := range member {
		if key != "mitre_atlas" && key != "owasp_llm" {
			return loadErr("field \"references\" carries undefined member %q", key)
		}
		if _, err := stringList(value, "references."+key); err != nil {
			return err
		}
	}
	return nil
}

// loadInformational validates the publisher metadata a rule may carry; the
// engine derives nothing from it ([OAR-DOC-29], [OAR-DOC-30]).
func loadInformational(obj map[string]any) error {
	for _, field := range []string{"emit", "category"} {
		if _, err := optionalString(obj, field); err != nil {
			return err
		}
	}
	for _, field := range []string{"context_schema", "evidence"} {
		if raw, present := obj[field]; present {
			if _, ok := asObject(raw); !ok {
				return loadErr("[OAR-DOC-30] field %q must be an object", field)
			}
		}
	}
	if raw, present := obj["scenarios"]; present {
		list, ok := raw.([]any)
		if !ok {
			return loadErr("[OAR-DOC-30] field \"scenarios\" must be a list of objects")
		}
		for _, entry := range list {
			if _, ok := asObject(entry); !ok {
				return loadErr("[OAR-DOC-30] field \"scenarios\" must be a list of objects")
			}
		}
	}
	return nil
}

// loadDetectorField reads `detector`, present if and only if kind is detector
// ([OAR-DOC-17]).
func loadDetectorField(obj map[string]any, rule *Rule, env *Environment) error {
	raw, present := obj["detector"]
	if rule.Kind != "detector" {
		if present {
			return loadErr("[OAR-DOC-17] rule %s has kind %s and must not carry \"detector\"", rule.Qualified, rule.Kind)
		}
		return nil
	}
	member, ok := asObject(raw)
	if !ok {
		return loadErr("[OAR-DOC-17] rule %s has kind detector and must carry \"detector\"", rule.Qualified)
	}
	for key := range member {
		if key != "ref" {
			return loadErr("field \"detector\" carries undefined member %q", key)
		}
	}
	ref, ok := member["ref"].(string)
	if !ok || !ruleDetectorRE.MatchString(ref) {
		return loadErr("[OAR-OPS-10] field \"detector.ref\" must be a detector:// reference")
	}
	if !env.Detectors[ref] {
		return loadErr("[OAR-OPS-10] rule %s references detector %s, which this engine cannot resolve", rule.Qualified, ref)
	}
	return nil
}

// resolveAnchor resolves `on`: a core anchor through the profile map, any
// other value as a host-native anchor ([OAR-CONF-3], [OAR-PROF-3],
// [OAR-PROF-4]).
func resolveAnchor(obj map[string]any, rule *Rule, env *Environment) error {
	on, ok := obj["on"].(string)
	if !ok || !anchorRE.MatchString(on) {
		return loadErr("[OAR-SEL-1] field \"on\" must name exactly one anchor, got %v", obj["on"])
	}
	if IsCoreAnchor(on) {
		local, supported := env.Anchors[on]
		if !supported {
			return loadErr("[OAR-PROF-3] rule %s targets core anchor %s, which this host declares unsupported", rule.Qualified, on)
		}
		rule.ResolvedAnchor = local
		return nil
	}
	if !env.HostAnchors[on] {
		return loadErr("[OAR-SEL-1] rule %s targets anchor %q, which this engine cannot resolve", rule.Qualified, on)
	}
	rule.ResolvedAnchor = on
	return nil
}

// loadRequiresFacts is fact negotiation ([OAR-DOC-23], [OAR-PROF-6],
// [OAR-CONF-4]). It returns the names beyond the core tier the rule may reach.
func loadRequiresFacts(obj map[string]any, rule *Rule, env *Environment) (map[string]bool, error) {
	reachable := map[string]bool{}
	names, err := stringList(obj["requires_facts"], "requires_facts")
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !factNameRE.MatchString(name) {
			return nil, loadErr("field \"requires_facts\" holds malformed name %q", name)
		}
		_, isFact := env.Facts[name]
		_, isFn := env.Functions[name]
		if !isFact && !isFn {
			return nil, loadErr("[OAR-DOC-23] rule %s requires fact %q, which this host does not provide", rule.Qualified, name)
		}
		reachable[name] = true
	}
	rule.RequiresFacts = names
	return reachable, nil
}

// selectorStringLists are the selector clauses holding a set of strings.
var selectorStringLists = []string{"tools", "profiles", "surfaces", "session_posture"}

// selectorNullable are the singular clauses whose null matches an unbound
// dimension.
var selectorNullable = []string{"surface", "phase", "workflow", "tool"}

func loadSelector(obj map[string]any, rule *Rule, env *Environment) error {
	member, ok := asObject(obj["selector"])
	if !ok {
		return loadErr("[OAR-DOC-2] field \"selector\" must be an object")
	}
	// [OAR-DOC-19] The deprecated top-level tools stands for selector.tools
	// when selector.tools is absent.
	if raw, present := obj["tools"]; present {
		if _, alsoSelected := member["tools"]; !alsoSelected {
			copied := map[string]any{}
			for k, v := range member {
				copied[k] = v
			}
			copied["tools"] = raw
			member = copied
		}
	}
	names := make([]string, 0, len(member))
	for name := range member {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, dimension := range names {
		raw := member[dimension]
		clause := SelectorClause{Dimension: dimension}
		switch {
		case contains(selectorNullable, dimension):
			if raw == nil {
				clause.Null = true
				break
			}
			s, ok := raw.(string)
			if !ok {
				return loadErr("[OAR-SEL-7] selector clause %q must be a string or null", dimension)
			}
			if dimension == "surface" && !contains([]string{"coordinator", "worker", "phase"}, s) {
				return loadErr("[OAR-SEL-7] selector clause \"surface\" must be coordinator, worker, phase, or null")
			}
			clause.Values = []string{s}
		case contains(selectorStringLists, dimension):
			values, err := stringList(raw, "selector."+dimension)
			if err != nil {
				return loadErr("[OAR-SEL-7] %s", err.Error())
			}
			clause.Values = values
		case dimension == "principal":
			if s, ok := raw.(string); ok {
				if !contains([]string{"human", "coordinator", "worker"}, s) {
					return loadErr("[OAR-SEL-7] selector clause \"principal\" names role %q, not human, coordinator, or worker", s)
				}
				clause.Values = []string{s}
				break
			}
			values, err := stringList(raw, "selector.principal")
			if err != nil {
				return loadErr("[OAR-SEL-7] selector clause \"principal\" must be a role or a list of caller ids")
			}
			clause.Values = values
		case dimension == "ledger":
			facts, ok := asObject(raw)
			if !ok {
				return loadErr("[OAR-SEL-7] selector clause \"ledger\" must be an object")
			}
			clause.Ledger = map[string][]string{}
			for name, want := range facts {
				decl, declared := env.Facts[name]
				// [OAR-SEL-5] A ledger clause names typed facts only.
				if declared && decl.Type != TypeString && decl.Type != TypeListString {
					return loadErr("[OAR-SEL-5] ledger clause names %s, whose type %s is neither string nor list<string>", name, decl.Type)
				}
				values, err := ledgerValues(want, name)
				if err != nil {
					return err
				}
				clause.Ledger[name] = values
				if declared {
					rule.Refs[name] = true
				}
			}
		default:
			// [OAR-SEL-7] The selector vocabulary is closed.
			return loadErr("[OAR-SEL-7] selector carries clause %q, which the selector schema does not define", dimension)
		}
		rule.Selector = append(rule.Selector, clause)
	}
	return nil
}

// ledgerValues reads the value a ledger clause accepts for one fact: a
// string or a list of strings.
func ledgerValues(want any, name string) ([]string, error) {
	if s, ok := want.(string); ok {
		return []string{s}, nil
	}
	return stringList(want, "selector.ledger."+name)
}

// loadWhen parses and type-checks the condition against the declared
// environment ([OAR-DOC-9], [OAR-CONF-5]).
func loadWhen(obj map[string]any, rule *Rule, env *Environment) error {
	raw, present := obj["when"]
	if !present {
		return nil
	}
	source, ok := raw.(string)
	if !ok {
		return loadErr("[OAR-DOC-9] field \"when\" must be a string")
	}
	ast, err := parseCondition(source)
	if err != nil {
		return loadErr("rule %s condition: %s", rule.Qualified, err.Error())
	}
	if env.ExpressionNodesMax > 0 {
		if nodes := countNodes(ast); nodes > env.ExpressionNodesMax {
			return loadErr("rule %s condition has %d parse-tree nodes, above this engine's limit of %d", rule.Qualified, nodes, env.ExpressionNodesMax)
		}
	}
	resultType, refs, err := checkCondition(ast, env)
	if err != nil {
		return loadErr("rule %s condition: %s", rule.Qualified, err.Error())
	}
	// [OAR-FACT-3] The rejection names the produced type and the word bool.
	if resultType != TypeBool && resultType != typeDyn {
		return loadErr("[OAR-FACT-3] rule %s condition produces %s, want bool", rule.Qualified, resultType)
	}
	for name := range refs {
		rule.Refs[name] = true
	}
	rule.When = ast
	return nil
}

// loadCopy reads the copy members and type-checks their bindings against the
// facts the rule reaches ([OAR-DOC-18], Appendix B).
func loadCopy(obj map[string]any, rule *Rule, env *Environment, reachable map[string]bool) error {
	for _, member := range copyMembers {
		raw, present := obj[member]
		if !present {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return loadErr("field %q must be a string", member)
		}
		rule.Copy[member] = s
	}
	// Copy reaches the facts the rule names in requires_facts and those its
	// condition and selector already reference.
	referenced := make(map[string]bool, len(rule.Refs))
	for name := range rule.Refs {
		referenced[name] = true
	}
	names := make([]string, 0, len(rule.Copy))
	for name := range rule.Copy {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, member := range names {
		bindings, err := ParseCopy(rule.Copy[member])
		if err != nil {
			return loadErr("[OAR-COPY-3] rule %s %s carries construct %s", rule.Qualified, member, err.Error())
		}
		for _, binding := range bindings {
			decl, declared := env.Facts[binding.Name]
			if !declared {
				return loadErr("[OAR-COPY-2] rule %s %s names %s, which is not a declared fact", rule.Qualified, member, binding.Name)
			}
			if binding.Interpolate && !copyInterpolatable(decl.Type) {
				return loadErr("[OAR-COPY-4] rule %s %s interpolates %s, whose type is %s", rule.Qualified, member, binding.Name, decl.Type)
			}
			// [OAR-COPY-5] A non-core fact named only in copy is reached
			// through requires_facts.
			if decl.Tier != TierCore && !reachable[binding.Name] && !referenced[binding.Name] {
				return loadErr("[OAR-COPY-5] rule %s %s names %s, which the rule does not reach through requires_facts", rule.Qualified, member, binding.Name)
			}
			rule.Refs[binding.Name] = true
		}
	}
	return nil
}

// loadFlow reads `flow` against the published activity window ([OAR-DOC-10],
// [OAR-FACT-10]).
func loadFlow(obj map[string]any, rule *Rule, env *Environment) error {
	raw, present := obj["flow"]
	if !present {
		return nil
	}
	flow, err := stringList(raw, "flow")
	if err != nil {
		return err
	}
	if len(flow) == 0 {
		return loadErr("[OAR-DOC-10] rule %s carries an empty \"flow\"", rule.Qualified)
	}
	if len(flow) > env.ActivityWindow {
		return loadErr("[OAR-FACT-10] rule %s carries a flow of %d steps, above the published activity window of %d",
			rule.Qualified, len(flow), env.ActivityWindow)
	}
	rule.Flow = flow
	return nil
}
