package oar

import (
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
	// snapshot precedes this occurrence's side-effects ([OAR-FIRE-5]).
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

// counterKeyFor keys a rule's counters by its identity; the store adds the
// session ([OAR-FIRE-4]).
func counterKeyFor(r *Rule, _ *GuardContext) string {
	if r == nil {
		return ""
	}
	return r.Qualified()
}

// counterOf reads another rule's counter, named by id in the reading rule's
// namespace, from the pre-occurrence snapshot ([OAR-FIRE-5], [OAR-FIRE-6]).
func (h *evalHolder) counterOf(id string, kind CounterKind) int64 {
	if h == nil || h.gc == nil || h.currentRule == nil {
		return 0
	}
	return h.getCounter(h.gc.Session.SessionID, oarcore.Identity(h.currentRule.Namespace, id), kind)
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
	env, err := capabilityEnvironment(InstalledEngineDeclaration())
	if err != nil {
		return false, err
	}
	return oarcore.EvaluateCondition(when, env, activation(gc), specObservationFuncs(holder))
}

func capabilityEnvironment(cap *EngineDeclaration) (*oarcore.Environment, error) {
	return cap.Environment()
}

func checkWhenAgainstSpec(when string, cap *EngineDeclaration) error {
	if cap == nil {
		cap = InstalledEngineDeclaration()
	}
	if strings.TrimSpace(when) == "" {
		return nil
	}
	env, err := capabilityEnvironment(cap)
	if err != nil {
		return err
	}
	return oarcore.CheckCondition(when, env)
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

// EvalPathOutsideScope is the path_outside_scope observation for a tool.
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

// Typed argument accessors return zero for absent or mismatched types.
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
