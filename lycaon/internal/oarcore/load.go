package oarcore

// The version gate, the closed field set, anchor resolution, capability
// negotiation, selector clauses, and a full type check of the condition all run
// at load, before any occurrence is evaluated.
//
// [OAR-OPS-3] Malformed documents, unknown identifiers, and type errors are
// load-time rejections and never route through `on_error`.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LoadError is a rule document, or a rule set, this engine refuses. Every
// rejection names the field, identifier, or capability at fault.
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

// definedFields is the closed document field set ([OAR-DOC-27]).
var definedFields = []string{
	"oar", "id", "namespace", "kind", "anchor", "selector", "when", "flow",
	"effect", "enforcement", "mandatory", "on_error", "on_fire", "counter_scope",
	"overrides", "requires", "transform", "detector", "copy", "references",
	"status", "related",
}

// copyMembers cannot affect decisions ([OAR-DOC-24]).
var copyMembers = []string{"title", "what", "cause", "why", "fix", "instead"}

// SelectorClause is one selector clause, named by a fact ([OAR-SEL-1]).
type SelectorClause struct {
	Fact   string
	Values []string
}

// TransformSpec describes a content mutation ([OAR-OPS-13]).
type TransformSpec struct {
	Action      string
	Target      string
	Replacement string
	// HasReplacement distinguishes an absent replacement from an empty one:
	// `redact` MAY omit it, and when it does the engine substitutes its own
	// placeholder rather than the empty string.
	HasReplacement bool
}

// CounterRef is one fire_count_of or breaker_count_of call in a condition: the
// built-in that read it, the literal the author wrote, and the qualified
// identifier that literal resolves to in the referencing rule's namespace
// ([OAR-FIRE-11]).
type CounterRef struct {
	Fn      string
	Counter string
	Literal string
	Target  string
}

// Rule is one loaded rule document.
type Rule struct {
	environment *Environment
	Namespace   string
	// Qualified is the identity of a rule: <namespace>/<id>, or the bare id when
	// namespace is absent ([OAR-DOC-8]). It is derivable from the document alone
	// and does not vary with the host that loaded it.
	Qualified string
	Kind      string
	Effect    string
	// ResolvedAnchor is the local anchor a core anchor resolves to through the
	// profile map ([OAR-PROF-4]).
	ResolvedAnchor string
	Selector       []SelectorClause
	When           *node
	CounterRefs    []CounterRef
	Transform      *TransformSpec
	Copy           map[string]string
	// Refs is every fact and function name the condition, selector,
	// counter_scope, and copy reference.
	Refs     map[string]bool
	Warnings []string
}

// ResolveRef resolves a bare or qualified rule reference within a referring
// namespace ([OAR-EVAL-13]).
func ResolveRef(ref, namespace string) string {
	if strings.Contains(ref, "/") {
		return ref
	}
	if namespace == "" {
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

// LoadRuleDocument loads one rule document against a declared environment.
func LoadRuleDocument(doc any, env *Environment) (*Rule, error) {
	obj, ok := asObject(doc)
	if !ok {
		return nil, loadErr("[OAR-DOC-31] a rule set member is not a rule document: expected a JSON object")
	}

	if err := checkVersion(obj); err != nil {
		return nil, err
	}
	if err := checkClosedWorld(obj); err != nil {
		return nil, err
	}

	// [OAR-DOC-2] The five obligatory fields. `oar` was already checked, because
	// [OAR-CONF-2] requires the version gate before any other processing.
	for _, required := range []string{"id", "kind", "anchor", "effect"} {
		if _, present := obj[required]; !present {
			return nil, loadErr("[OAR-DOC-2] rule document is missing required field %q", required)
		}
	}

	id, ok := obj["id"].(string)
	if !ok || !idRE.MatchString(id) {
		return nil, loadErr("[OAR-DOC-6] field \"id\" does not match ^[A-Z][A-Z0-9_]*$: %v", obj["id"])
	}

	namespace := ""
	if raw, present := obj["namespace"]; present {
		ns, ok := raw.(string)
		if !ok || !namespaceRE.MatchString(ns) {
			return nil, loadErr("field \"namespace\" is not a publisher scope: %v", raw)
		}
		namespace = ns
	}
	qualified := id
	if namespace != "" {
		qualified = namespace + "/" + id
	}

	kind, ok := obj["kind"].(string)
	if !ok || !contains(kinds, kind) {
		return nil, loadErr("[OAR-DOC-10] field \"kind\" is %v, not one of %s", obj["kind"], strings.Join(kinds, ", "))
	}

	effect, ok := obj["effect"].(string)
	if !ok || !contains(effects, effect) {
		return nil, loadErr("[OAR-DOC-11] field \"effect\" is %v, not one of %s", obj["effect"], strings.Join(effects, ", "))
	}

	anchorRaw, ok := obj["anchor"].(string)
	if !ok || !anchorRE.MatchString(anchorRaw) {
		return nil, loadErr("field \"anchor\" must name exactly one lifecycle moment")
	}

	rule := &Rule{
		Namespace:   namespace,
		Qualified:   qualified,
		Kind:        kind,
		Effect:      effect,
		Copy:        map[string]string{},
		Refs:        map[string]bool{},
		environment: env,
	}

	if err := checkScalarFields(obj); err != nil {
		return nil, err
	}
	if err := loadRelatedAndCopy(obj, rule); err != nil {
		return nil, err
	}
	if err := loadDetectorField(obj, rule, env); err != nil {
		return nil, err
	}
	if err := loadTransformField(obj, rule, env); err != nil {
		return nil, err
	}

	// [OAR-CONF-3], [OAR-PROF-4] Resolve the anchor: a core anchor identifier
	// through the profile map, any other value as a host-native anchor.
	if IsCoreAnchor(anchorRaw) {
		local, supported := env.Anchors[anchorRaw]
		if !supported {
			return nil, loadErr("[OAR-PROF-4] rule %s targets core anchor %s, which this host declares unsupported", qualified, anchorRaw)
		}
		rule.ResolvedAnchor = local
	} else {
		// [OAR-PROF-5], [OAR-DOC-12] A host-native anchor loads only when the
		// declared catalogue lists it; any other value is rejected by name.
		if !env.HostAnchors[anchorRaw] {
			return nil, loadErr("[OAR-PROF-5] rule %s targets anchor %q, which is neither a core anchor nor in the declared host anchor catalogue", qualified, anchorRaw)
		}
		rule.ResolvedAnchor = anchorRaw
	}

	reachable, err := loadRequires(obj, rule, env)
	if err != nil {
		return nil, err
	}
	if err := loadSelector(obj, rule, env, reachable); err != nil {
		return nil, err
	}
	if err := loadWhen(obj, rule, env, reachable); err != nil {
		return nil, err
	}
	if err := loadCounterScope(obj, rule, env, reachable); err != nil {
		return nil, err
	}
	if rule.Transform != nil && rule.Transform.Target != "content" {
		name := rule.Transform.Target
		if !reachable[name] {
			return nil, loadErr("[OAR-FACT-20] transform target %s is outside requires", name)
		}
		rule.Refs[name] = true
	}
	if err := loadCopyBindings(rule, env, reachable); err != nil {
		return nil, err
	}
	if err := loadFlow(obj, rule, env); err != nil {
		return nil, err
	}
	return rule, nil
}

// checkVersion is the version gate. [OAR-CONF-2] requires `oar` to be checked
// before any other processing of the document, so a bad version wins over a bad
// field.
func checkVersion(obj map[string]any) error {
	raw, present := obj["oar"]
	if !present {
		return loadErr("rule document is missing required field \"oar\"")
	}
	oar, ok := raw.(string)
	if !ok || !versionRE.MatchString(oar) {
		return loadErr("field \"oar\" must be a version string of the form <major>.<minor>")
	}
	parts := strings.SplitN(oar, ".", 2)
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major != oarMajor {
		// [OAR-DOC-4] An unsupported major is refused, never downgraded or
		// coerced.
		return loadErr("[OAR-DOC-4] oar %s names a major version this engine does not implement (%d.%d)", oar, oarMajor, oarMinor)
	}
	if minor > oarMinor {
		// [OAR-DOC-5] A greater minor may narrow the rule with a field this engine
		// would ignore, so refusing to load is the only visible failure mode.
		return loadErr("[OAR-DOC-5] oar %s names a minor version above this engine's own (%d.%d)", oar, oarMajor, oarMinor)
	}
	return nil
}

func checkClosedWorld(obj map[string]any) error {
	names := make([]string, 0, len(obj))
	for key := range obj {
		names = append(names, key)
	}
	// Sorted so the rejection names the same undefined field on every run.
	sort.Strings(names)
	for _, key := range names {
		if contains(definedFields, key) {
			continue
		}
		// [OAR-DOC-26] An unrecognised x- extension is ignored, and MUST NOT
		// affect selection, evaluation, ordering, precedence, or the decision.
		if extensionRE.MatchString(key) {
			continue
		}
		return loadErr("[OAR-DOC-27] rule document carries undefined field %q", key)
	}
	return nil
}

// checkScalarFields validates the fields the host reads from its own rule
// model; this engine checks their shape only.
func checkScalarFields(obj map[string]any) error {
	if raw, present := obj["enforcement"]; present {
		v, ok := raw.(string)
		if !ok || !contains(enforcements, v) {
			return loadErr("field \"enforcement\" must be one of %s", strings.Join(enforcements, ", "))
		}
	}
	if raw, present := obj["mandatory"]; present {
		if _, ok := raw.(bool); !ok {
			return loadErr("field \"mandatory\" must be a boolean")
		}
	}
	if raw, present := obj["on_error"]; present {
		v, ok := raw.(string)
		if !ok {
			return loadErr("field \"on_error\" must be a string")
		}
		if v != "fail_closed" && v != "fail_open" && !idRE.MatchString(v) && !qualifiedRefRE.MatchString(v) {
			return loadErr("field \"on_error\" must be fail_closed, fail_open, or a rule identifier, got %q", v)
		}
	}
	if raw, present := obj["on_fire"]; present {
		actions, err := stringList(raw, "on_fire")
		if err != nil {
			return err
		}
		for _, action := range actions {
			if !contains(onFireActions, action) {
				// [OAR-FIRE-1] The side-effect vocabulary is closed.
				return loadErr("[OAR-FIRE-1] field \"on_fire\" names undefined side-effect %q", action)
			}
		}
	}
	if raw, present := obj["overrides"]; present {
		refs, err := stringList(raw, "overrides")
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if !idRE.MatchString(ref) && !qualifiedRefRE.MatchString(ref) {
				return loadErr("field \"overrides\" holds malformed rule reference %q", ref)
			}
		}
	}
	if raw, present := obj["status"]; present {
		v, ok := raw.(string)
		if !ok || !contains(statuses, v) {
			return loadErr("field \"status\" must be one of %s", strings.Join(statuses, ", "))
		}
	}
	return nil
}

func loadRelatedAndCopy(obj map[string]any, rule *Rule) error {
	if raw, present := obj["related"]; present {
		list, ok := raw.([]any)
		if !ok {
			return loadErr("field \"related\" must be a list")
		}
		for _, entry := range list {
			member, ok := asObject(entry)
			if !ok {
				return loadErr("field \"related\" holds a non-object")
			}
			for key := range member {
				if key != "id" && key != "type" {
					return loadErr("field \"related\" carries undefined member %q", key)
				}
			}
			// [OAR-DOC-30] Related targets need not be loaded.
			rid, ok := member["id"].(string)
			if !ok || (!idRE.MatchString(rid) && !qualifiedRefRE.MatchString(rid)) {
				return loadErr("field \"related\" holds malformed rule reference %v", member["id"])
			}
			rtype, ok := member["type"].(string)
			if !ok || !contains(relatedTypes, rtype) {
				return loadErr("field \"related\" type must be one of %s", strings.Join(relatedTypes, ", "))
			}
		}
	}

	if raw, present := obj["copy"]; present {
		member, ok := asObject(raw)
		if !ok {
			return loadErr("field \"copy\" must be an object")
		}
		for key, value := range member {
			// [OAR-DOC-24] copy carries a closed set of presentation strings.
			if !contains(copyMembers, key) {
				return loadErr("[OAR-DOC-24] field \"copy\" carries undefined member %q", key)
			}
			s, ok := value.(string)
			if !ok {
				return loadErr("field \"copy.%s\" must be a string", key)
			}
			rule.Copy[key] = s
		}
	}

	// [OAR-DOC-25] references is an interoperability taxonomy and MUST NOT affect
	// any decision. It is validated for shape and then never read again.
	if raw, present := obj["references"]; present {
		member, ok := asObject(raw)
		if !ok {
			return loadErr("field \"references\" must be an object")
		}
		for key, value := range member {
			if _, err := stringList(value, "references."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadDetectorField reads `detector`. [OAR-DOC-23] it is present if and only if
// kind is detector.
func loadDetectorField(obj map[string]any, rule *Rule, env *Environment) error {
	raw, present := obj["detector"]
	if rule.Kind != "detector" {
		if present {
			return loadErr("[OAR-DOC-23] rule %s has kind %s and must not carry \"detector\"", rule.Qualified, rule.Kind)
		}
		return nil
	}
	member, ok := asObject(raw)
	if !ok {
		return loadErr("[OAR-DOC-23] rule %s has kind detector and must carry \"detector\"", rule.Qualified)
	}
	for key := range member {
		if key != "ref" {
			return loadErr("field \"detector\" carries undefined member %q", key)
		}
	}
	ref, ok := member["ref"].(string)
	if !ok || !ruleDetectorRE.MatchString(ref) {
		return loadErr("field \"detector.ref\" must be a detector:// reference")
	}
	// [OAR-OPS-12] An unregistered detector is a load-time rejection, not a
	// run-time on_error.
	if !env.Detectors[ref] {
		return loadErr("[OAR-OPS-12] rule %s references detector %s, which is not registered", rule.Qualified, ref)
	}
	return nil
}

// loadTransformField reads `transform`. [OAR-DOC-22] it is present if and only
// if effect is transform.
func loadTransformField(obj map[string]any, rule *Rule, env *Environment) error {
	raw, present := obj["transform"]
	if rule.Effect != "transform" {
		if present {
			return loadErr("[OAR-DOC-22] rule %s has effect %s and must not carry \"transform\"", rule.Qualified, rule.Effect)
		}
		return nil
	}
	// [OAR-OPS-18] An engine that does not implement content mutation refuses the
	// rule at load, rather than treating it as a block or ignoring it.
	if !env.SupportsTransform {
		return loadErr("[OAR-OPS-18] rule %s has effect transform, and this engine declares supports_transform false", rule.Qualified)
	}
	member, ok := asObject(raw)
	if !ok {
		return loadErr("[OAR-DOC-22] rule %s has effect transform and must carry \"transform\"", rule.Qualified)
	}
	for key := range member {
		if key != "action" && key != "target" && key != "replacement" {
			return loadErr("field \"transform\" carries undefined member %q", key)
		}
	}
	action, ok := member["action"].(string)
	if !ok || !contains(transformActions, action) {
		return loadErr("field \"transform.action\" must be one of %s", strings.Join(transformActions, ", "))
	}
	target, ok := member["target"].(string)
	if !ok {
		return loadErr("field \"transform.target\" must be a string")
	}
	spec := &TransformSpec{Action: action, Target: target}
	if rawReplacement, hasReplacement := member["replacement"]; hasReplacement {
		replacement, ok := rawReplacement.(string)
		if !ok {
			return loadErr("field \"transform.replacement\" must be a string")
		}
		spec.Replacement = replacement
		spec.HasReplacement = true
	}
	// [OAR-OPS-13] An annotation with nothing to annotate with is not a
	// transform, so replace and annotate must carry a replacement; redact may
	// omit one, and the engine then substitutes its own placeholder.
	if (action == "replace" || action == "annotate") && !spec.HasReplacement {
		return loadErr("[OAR-OPS-13] field \"transform.replacement\" is required for action %s", action)
	}
	// [OAR-OPS-14] target names the whole content, or a list<map> fact whose
	// members carry the spans to act on. Anything else is a load-time rejection.
	if target != "content" {
		fact, declared := env.Facts[target]
		if !declared || fact.Type != TypeListMap {
			return loadErr("[OAR-OPS-14] field \"transform.target\" names %q, which is neither \"content\" nor a declared list<map> fact", target)
		}
	}
	rule.Transform = spec
	return nil
}

// loadRequires is capability negotiation ([OAR-DOC-21], [OAR-FACT-19]). It
// returns the set of names the rule may reach.
func loadRequires(obj map[string]any, rule *Rule, env *Environment) (map[string]bool, error) {
	reachable := map[string]bool{}
	raw, present := obj["requires"]
	if !present {
		return reachable, nil
	}
	member, ok := asObject(raw)
	if !ok {
		return nil, loadErr("field \"requires\" must be an object")
	}
	for key := range member {
		if key != "profiles" && key != "facts" {
			return nil, loadErr("field \"requires\" carries undefined member %q", key)
		}
	}
	requiresProfiles, err := stringList(member["profiles"], "requires.profiles")
	if err != nil {
		return nil, err
	}
	requiresFacts, err := stringList(member["facts"], "requires.facts")
	if err != nil {
		return nil, err
	}
	for _, name := range requiresFacts {
		if !factNameRE.MatchString(name) {
			return nil, loadErr("field \"requires.facts\" holds malformed name %q", name)
		}
	}
	for _, profile := range requiresProfiles {
		if !env.Profiles[profile] {
			return nil, loadErr("[OAR-FACT-19] rule %s requires capability profile %q, which this host does not provide", rule.Qualified, profile)
		}
		def := profiles[profile]
		for name := range def.Facts {
			reachable[name] = true
		}
		for name := range def.Functions {
			reachable[name] = true
		}
	}
	for _, name := range requiresFacts {
		_, isFact := env.Facts[name]
		_, isFn := env.Functions[name]
		if !isFact && !isFn {
			return nil, loadErr("[OAR-FACT-19] rule %s requires fact %q, which this host does not provide", rule.Qualified, name)
		}
		reachable[name] = true
	}
	return reachable, nil
}

func loadSelector(obj map[string]any, rule *Rule, env *Environment, reachable map[string]bool) error {
	raw, present := obj["selector"]
	if !present {
		return nil
	}
	member, ok := asObject(raw)
	if !ok {
		return loadErr("field \"selector\" must be an object")
	}
	names := make([]string, 0, len(member))
	for name := range member {
		names = append(names, name)
	}
	// Clauses are conjunctive ([OAR-SEL-4]); sorting fixes which one a rejection
	// names.
	sort.Strings(names)
	for _, factName := range names {
		fact, declared := env.Facts[factName]
		// [OAR-SEL-3] A clause naming a fact the engine does not declare, or one
		// whose type is neither string nor list<string>, is a load error — never a
		// silent match and never a silent non-match.
		if !declared {
			return loadErr("[OAR-SEL-3] selector clause %q names a fact this engine does not declare", factName)
		}
		if fact.Type != TypeString && fact.Type != TypeListString {
			return loadErr("[OAR-SEL-3] selector clause %q names a fact of type %s, which is neither string nor list<string>", factName, fact.Type)
		}
		if fact.Tier != TierCore && !reachable[factName] {
			// [OAR-FACT-20] A selector reference outside the core tier must be
			// reached through requires, exactly as a condition reference must.
			return loadErr("[OAR-FACT-20] selector clause %q is outside the core tier and the rule does not reach it through requires", factName)
		}
		values, err := stringList(member[factName], "selector."+factName)
		if err != nil {
			return err
		}
		// [OAR-SEL-6] A clause whose value is the empty list matches nothing, so
		// the rule is never selected. The rule loads, and the engine warns.
		if len(values) == 0 {
			rule.Warnings = append(rule.Warnings,
				fmt.Sprintf("selector clause %q is empty, so rule %s is never selected", factName, rule.Qualified))
			values = []string{}
		}
		rule.Refs[factName] = true
		rule.Selector = append(rule.Selector, SelectorClause{Fact: factName, Values: values})
	}
	return nil
}

// loadWhen parses and type-checks the condition against the declared
// environment ([OAR-DOC-14], [OAR-CONF-5]).
func loadWhen(obj map[string]any, rule *Rule, env *Environment, reachable map[string]bool) error {
	raw, present := obj["when"]
	if !present {
		return nil
	}
	source, ok := raw.(string)
	if !ok {
		return loadErr("field \"when\" must be a string")
	}
	ast, err := parseCondition(source)
	if err != nil {
		return loadErr("rule %s condition: %s", rule.Qualified, err.Error())
	}
	// [OAR-EXPR-17] The declared parse-tree ceiling.
	if nodes := countNodes(ast); nodes > env.ExpressionNodesMax {
		return loadErr("[OAR-EXPR-17] rule %s condition has %d parse-tree nodes, above the declared expression_nodes_max of %d",
			rule.Qualified, nodes, env.ExpressionNodesMax)
	}
	resultType, refs, err := checkCondition(ast, env, reachable)
	if err != nil {
		return loadErr("rule %s condition: %s", rule.Qualified, err.Error())
	}
	// [OAR-FACT-4] The rejection names the produced type and the word bool.
	if resultType != TypeBool {
		return loadErr("[OAR-FACT-4] rule %s condition produces %s, want bool", rule.Qualified, resultType)
	}
	for name := range refs {
		rule.Refs[name] = true
	}
	rule.When = ast

	// [OAR-FIRE-11] The type checker already required each counter reader's
	// argument to be a string literal, so every reference resolves statically.
	walkNodes(ast, func(n *node) {
		if n.kind != nodeCall {
			return
		}
		counter, isReader := counterReaders[n.strValue]
		if !isReader || len(n.items) == 0 || n.items[0].kind != nodeString {
			return
		}
		literal := n.items[0].strValue
		rule.CounterRefs = append(rule.CounterRefs, CounterRef{
			Fn:      n.strValue,
			Counter: counter,
			Literal: literal,
			Target:  ResolveRef(literal, rule.Namespace),
		})
	})
	return nil
}

// loadCounterScope reads `counter_scope`: the declared string fact whose value
// keys this rule's counters ([OAR-DOC-32], [OAR-FIRE-10]).
func loadCounterScope(obj map[string]any, rule *Rule, env *Environment, reachable map[string]bool) error {
	raw, present := obj["counter_scope"]
	if !present {
		return nil
	}
	name, ok := raw.(string)
	if !ok || !factNameRE.MatchString(name) {
		return loadErr("field \"counter_scope\" must name a declared fact, got %v", raw)
	}
	decl, declared := env.Facts[name]
	if !declared {
		return loadErr("[OAR-FIRE-10] rule %s names counter_scope %s, which this engine does not declare", rule.Qualified, name)
	}
	if decl.Type != TypeString {
		return loadErr("[OAR-FIRE-10] rule %s names counter_scope %s, whose type is %s and not string", rule.Qualified, name, decl.Type)
	}
	if decl.Tier != TierCore && !reachable[name] {
		return loadErr("[OAR-FIRE-10] rule %s names counter_scope %s, which is outside the core tier and the rule does not reach it through requires",
			rule.Qualified, name)
	}
	// The scope fact is observed wherever the rule's counters move, so it counts
	// as a reference for fact production and the [OAR-PROF-8] portability report.
	rule.Refs[name] = true
	return nil
}

// loadCopyBindings type-checks each copy member after requires is known
// ([OAR-COPY-2], [OAR-COPY-3], [OAR-COPY-4], [OAR-COPY-5]).
func loadCopyBindings(rule *Rule, env *Environment, reachable map[string]bool) error {
	names := make([]string, 0, len(rule.Copy))
	for name := range rule.Copy {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, member := range names {
		bindings, err := ParseCopy(rule.Copy[member])
		if err != nil {
			return loadErr("[OAR-COPY-3] rule %s copy.%s carries construct %s", rule.Qualified, member, err.Error())
		}
		for _, binding := range bindings {
			decl, declared := env.Facts[binding.Name]
			if !declared {
				return loadErr("[OAR-COPY-2] rule %s copy.%s names %s, which this engine does not declare",
					rule.Qualified, member, binding.Name)
			}
			if binding.Interpolate && !copyInterpolatable(decl.Type) {
				return loadErr("[OAR-COPY-4] rule %s copy.%s interpolates %s, whose type is %s",
					rule.Qualified, member, binding.Name, decl.Type)
			}
			if decl.Tier != TierCore && !reachable[binding.Name] {
				return loadErr("[OAR-FACT-20] copy names %s, which is outside the core tier and the rule does not reach it through requires",
					binding.Name)
			}
			rule.Refs[binding.Name] = true
		}
	}
	return nil
}

// loadFlow reads `flow` against the declared activity window ([OAR-DOC-15],
// [OAR-FACT-13], [OAR-PROF-3]).
func loadFlow(obj map[string]any, rule *Rule, env *Environment) error {
	raw, present := obj["flow"]
	if !present {
		return nil
	}
	flow, err := stringList(raw, "flow")
	if err != nil {
		return err
	}
	// [OAR-DOC-15] The empty list has no single reading (vacuous or impossible
	// match), so a present flow must be non-empty.
	if len(flow) == 0 {
		return loadErr("[OAR-DOC-15] rule %s carries an empty \"flow\"; a flow that is present must be non-empty", rule.Qualified)
	}
	if env.ActivityWindow == 0 {
		return loadErr("[OAR-PROF-3] rule %s carries flow, and this host declares activity_window 0", rule.Qualified)
	}
	if len(flow) > env.ActivityWindow {
		return loadErr("[OAR-FACT-13] rule %s carries a flow of %d steps, above the declared activity_window of %d",
			rule.Qualified, len(flow), env.ActivityWindow)
	}
	return nil
}
