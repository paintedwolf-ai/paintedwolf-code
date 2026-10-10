package oar

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// SupportedSpecVersion is the only accepted major.minor for a rule's oar marker.
const SupportedSpecVersion = "1.0"

// evalHolder holds runtime state for one anchor occurrence.
type evalHolder struct {
	gc          *GuardContext
	counters    *CounterStore
	rules       *RuleSet
	currentRule *Rule
	// snapshot precedes this occurrence's side-effects ([OAR-FIRE-6]).
	snapshot map[string]map[CounterKind]int64
}

func newEvalHolder(gc *GuardContext, rules *RuleSet, counters *CounterStore) *evalHolder {
	h := &evalHolder{}
	h.gc = gc
	h.rules = rules
	h.counters = counters
	if gc != nil && counters != nil {
		h.beginOccurrence(counters, gc.Session.SessionID)
	}
	return h
}

func (h *evalHolder) set(gc *GuardContext) { h.gc = gc }
func (h *evalHolder) get() *GuardContext   { return h.gc }

func (h *evalHolder) setCurrentRule(r *Rule) {
	if h != nil {
		h.currentRule = r
	}
}

// counterOf reads kind for ruleID in the evaluating session. An absent store or
// session yields 0, the same value an unfired rule has ([OAR-FIRE-11]).
func (h *evalHolder) counterOf(ruleID string, kind CounterKind) int64 {
	if h == nil {
		return 0
	}
	gc := h.get()
	store := h.counters
	if gc == nil || store == nil || gc.Session.SessionID == "" {
		return 0
	}
	ns := ""
	if current := h.currentRule; current != nil {
		ns = current.Namespace
	}
	target := resolveCounterTarget(h.rules, ruleID, ns)
	key := ruleID
	if target != nil {
		key = counterKeyFor(target, gc)
	}
	return h.getCounter(gc.Session.SessionID, key, kind)
}

func (h *evalHolder) beginOccurrence(store *CounterStore, sessionID string) {
	if h == nil {
		return
	}
	h.snapshot = store.CloneSession(sessionID)
}

func (h *evalHolder) getCounter(sessionID, key string, kind CounterKind) int64 {
	if h != nil && h.snapshot != nil {
		if m, ok := h.snapshot[key]; ok {
			return m[kind]
		}
		return 0
	}
	if h == nil {
		return 0
	}
	store := h.counters
	if store == nil {
		return 0
	}
	return store.Get(sessionID, key, kind)
}

func resolveCounterTarget(rs *RuleSet, ref, namespace string) *Rule {
	if rs == nil {
		return nil
	}
	qualified := ref
	if !strings.Contains(ref, "/") && namespace != "" {
		qualified = namespace + "/" + ref
	}
	for _, r := range rs.All() {
		if r.Qualified() == qualified {
			return r
		}
	}
	return nil
}

func counterKeyFor(r *Rule, gc *GuardContext) string {
	if r == nil {
		return ""
	}
	q := r.Qualified()
	if strings.TrimSpace(r.CounterScope) == "" {
		return q
	}
	scope := scopedFactString(gc, r.CounterScope)
	return q + "#" + scope
}

// scopedFactString returns the string value a rule observes for the named fact.
func scopedFactString(gc *GuardContext, name string) string {
	value, _ := activation(gc)[name].(string)
	return value
}

func (h *evalHolder) counterScope(target *Rule) (string, error) {
	if target == nil {
		return "", fmt.Errorf("[OAR-FIRE-11] unknown counter target")
	}
	if target.CounterScope == "" {
		return target.Qualified(), nil
	}
	if err := h.gc.Ensure(target.CounterScope); err != nil {
		return "", err
	}
	facts := activation(h.gc)
	value := facts[target.CounterScope]
	if target.document != nil {
		var err error
		value, err = target.document.Fact(target.CounterScope, facts)
		if err != nil {
			return "", err
		}
	}
	if value == nil {
		value = ""
	}
	scope, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("[OAR-FACT-26] %s must be a string", target.CounterScope)
	}
	return target.Qualified() + "#" + scope, nil
}

func (h *evalHolder) readCounter(ref string, kind CounterKind) (any, error) {
	namespace := ""
	if h.currentRule != nil {
		namespace = h.currentRule.Namespace
	}
	target := resolveCounterTarget(h.rules, ref, namespace)
	key, err := h.counterScope(target)
	if err != nil {
		return nil, err
	}
	return h.getCounter(h.gc.Session.SessionID, key, kind), nil
}

func specObservationFuncs(holder *evalHolder) map[string]func(any) (any, error) {
	out := make(map[string]func(any) (any, error), len(observationFns))
	for _, f := range observationFns {
		fn := f
		name := publishedName(fn.name, fn.tier)
		out[name] = func(arg any) (any, error) {
			if gc := holder.get(); gc != nil && gc.Published != nil {
				if v, ok := gc.Published[name]; ok {
					return v, nil
				}
			}
			s, ok := arg.(string)
			if !ok {
				return zeroForFactType(fn.ret), nil
			}
			if name == "fire_count_of" {
				return holder.readCounter(s, CounterFire)
			}
			if name == "breaker_count_of" {
				return holder.readCounter(s, CounterBreaker)
			}
			return fn.bind(holder, s), nil
		}
	}
	return out
}

func publishedName(name string, tier FactTier) string {
	if tier == FactTierHost {
		return oarcopy.HostFactNamespace + "." + name
	}
	return name
}

func zeroForFactType(t oarcore.FactType) any {
	switch t {
	case oarcore.TypeString:
		return ""
	case oarcore.TypeInt:
		return int64(0)
	default:
		return false
	}
}

// EvaluateCondition evaluates a standalone condition.
func EvaluateCondition(when string, gc *GuardContext) (bool, error) {
	return evalCondition(newEvalHolder(gc, nil, nil), when, gc)
}

func evalCondition(holder *evalHolder, when string, gc *GuardContext) (bool, error) {
	if strings.TrimSpace(when) == "" {
		return true, nil
	}
	holder.set(gc)
	env, err := capabilityEnvironment(InstalledCapabilityDocument())
	if err != nil {
		return false, err
	}
	return oarcore.EvaluateCondition(when, env, activation(gc), specObservationFuncs(holder))
}

func capabilityEnvironment(cap *CapabilityDocument) (*oarcore.Environment, error) {
	raw, err := json.Marshal(cap)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return oarcore.LoadCapability(document)
}

func specReachable(env *oarcore.Environment) map[string]bool {
	out := map[string]bool{}
	if env == nil {
		return out
	}
	for name := range env.Facts {
		out[name] = true
	}
	for name := range env.Functions {
		out[name] = true
	}
	return out
}

func checkWhenAgainstSpec(when string, cap *CapabilityDocument) error {
	if cap == nil {
		cap = InstalledCapabilityDocument()
	}
	if strings.TrimSpace(when) == "" {
		return nil
	}
	env, err := capabilityEnvironment(cap)
	if err != nil {
		return err
	}
	return oarcore.CheckCondition(when, env, specReachable(env))
}

func evalRuleWhen(holder *evalHolder, r *Rule, gc *GuardContext) (bool, error) {
	if r == nil {
		return true, nil
	}
	holder.set(gc)
	if r.document != nil {
		return r.document.EvaluateCondition(activation(gc), specObservationFuncs(holder))
	}
	return evalCondition(holder, r.When, gc)
}

// EvalPathOutsideScope is the observation for path_outside_scope(tool).
func EvalPathOutsideScope(gc *GuardContext, tool string) bool {
	if gc == nil {
		return false
	}
	if gc.Access.PathOutsideScopeByTool != nil {
		if v, ok := gc.Access.PathOutsideScopeByTool[tool]; ok {
			return v
		}
	}
	if tool == gc.Invocation.Tool {
		return gc.Access.PathOutsideScope
	}
	return false
}

// Typed argument accessors return zero for absent or mismatched types ([OAR-EXPR-19]).
func EvalToolArgString(gc *GuardContext, key string) string {
	if gc == nil {
		return ""
	}
	s, _ := gc.Invocation.ToolArgs[key].(string)
	return s
}

func EvalToolArgInt(gc *GuardContext, key string) int64 {
	if gc == nil {
		return 0
	}
	switch v := gc.Invocation.ToolArgs[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		// Decoded floats count as integers only when they have no fractional part.
		if v == float64(int64(v)) {
			return int64(v)
		}
	}
	return 0
}

func EvalToolArgBool(gc *GuardContext, key string) bool {
	if gc == nil {
		return false
	}
	b, _ := gc.Invocation.ToolArgs[key].(bool)
	return b
}

// EvalSourceIncludes is the observation for source_includes(id).
func EvalSourceIncludes(gc *GuardContext, id string) bool {
	if gc == nil || gc.Source.SourceIncludes == nil {
		return false
	}
	return gc.Source.SourceIncludes[id]
}

// EvalHostResourceStatus and EvalHostResourcePolicy expose resource catalog state.
func EvalHostResourceStatus(gc *GuardContext, id string) string {
	if gc == nil || gc.Access.HostResourceStatus == nil {
		return ""
	}
	if status, ok := gc.Access.HostResourceStatus[id]; ok {
		return status
	}
	return ""
}

func EvalHostResourcePolicy(gc *GuardContext, id string) string {
	if gc == nil || gc.Access.HostResourcePolicy == nil {
		return ""
	}
	if policy, ok := gc.Access.HostResourcePolicy[id]; ok {
		return policy
	}
	return ""
}

// FlowMatches reports whether pattern is an ordered subsequence of recent tool names.
func FlowMatches(recent, pattern []string) bool {
	if len(pattern) == 0 {
		return true
	}
	i := 0
	for _, name := range recent {
		if name == pattern[i] {
			i++
			if i == len(pattern) {
				return true
			}
		}
	}
	return false
}
