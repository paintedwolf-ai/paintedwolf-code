package oar

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/oarcore"
)

// RuleBranchHit records condition and next-step evaluations for one OAR branch,
// separating production GuardContext witnesses from mock scenario fixtures.
type RuleBranchHit struct {
	RuleID        string
	BranchID      string
	StepIndex     int
	TakenTrue     int
	TakenFalse    int
	ScenarioTrue  int
	ScenarioFalse int
}

// RuleBranchRecorder aggregates OAR when and next-steps outcomes under Mu when
// stored on ActiveRuleBranchRecorder. Evaluation performs an atomic pointer
// load and skips recording when nil.
//
// With ObserveConditions set, each production evaluation of a guarded branch
// also records its atoms' values in Conditions, grouped by Partition; each
// vector keeps the Label of its first observation.
type RuleBranchRecorder struct {
	Mu                sync.Mutex
	Hits              map[string]*RuleBranchHit
	ObserveConditions bool
	Partition         func(gc *GuardContext) string
	Label             func() string
	Conditions        map[string]*RuleConditionHit
}

// RuleConditionVector is one observed combination of atom values for a
// branch condition and the decision the condition produced. Recomposes reports
// that every atom is known and the connectives over them give Decision.
type RuleConditionVector struct {
	Atoms      []oarcore.Truth
	Decision   bool
	Recomposes bool
	Partition  string
	Label      string
	Count      int
}

// RuleConditionHit collects the distinct vectors observed at one rule or step
// condition.
type RuleConditionHit struct {
	RuleID   string
	BranchID string
	Source   string
	Atoms    []string
	Vectors  []*RuleConditionVector
	// Decide gives the condition's decision for atom truths keyed by atom text.
	Decide func(map[string]bool) bool
}

// ActiveRuleBranchRecorder is the opt-in recorder consulted by rule and next-step evaluation.
var ActiveRuleBranchRecorder atomic.Pointer[RuleBranchRecorder]

// NextStepObserver receives each non-scenario ResolvedSteps produced by
// ResolveNextSteps while stored in ActiveNextStepObservers.
type NextStepObserver func(r *Rule, resolved ResolvedSteps)

// ActiveNextStepObservers holds opt-in NextStepObserver values keyed by their
// owner. Keyed registrations let concurrent observers come and go without
// displacing each other.
var ActiveNextStepObservers sync.Map

func hasActiveNextStepObserver(r *Rule, gc *GuardContext) bool {
	if r == nil || (gc != nil && gc.FromScenario) {
		return false
	}
	found := false
	ActiveNextStepObservers.Range(func(_, v any) bool {
		if obs, ok := v.(NextStepObserver); ok && obs != nil {
			found = true
			return false
		}
		return true
	})
	return found
}

func recordResolvedNextSteps(r *Rule, gc *GuardContext, resolved ResolvedSteps) {
	if r == nil || (gc != nil && gc.FromScenario) {
		return
	}
	ActiveNextStepObservers.Range(func(_, v any) bool {
		if obs, ok := v.(NextStepObserver); ok && obs != nil {
			obs(r, resolved)
		}
		return true
	})
}

func recordRuleBranch(ruleID, branchID string, stepIndex int, gc *GuardContext, taken bool) {
	rec := ActiveRuleBranchRecorder.Load()
	if rec == nil || ruleID == "" {
		return
	}
	fromScenario := gc != nil && gc.FromScenario
	rec.record(ruleID, branchID, stepIndex, taken, fromScenario)
}

func (r *RuleBranchRecorder) record(ruleID, branchID string, stepIndex int, taken, fromScenario bool) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.Hits == nil {
		r.Hits = make(map[string]*RuleBranchHit)
	}
	key := ruleID + "#" + branchID
	hit, ok := r.Hits[key]
	if !ok {
		hit = &RuleBranchHit{
			RuleID:    ruleID,
			BranchID:  branchID,
			StepIndex: stepIndex,
		}
		r.Hits[key] = hit
	}
	switch {
	case fromScenario && taken:
		hit.ScenarioTrue++
	case fromScenario && !taken:
		hit.ScenarioFalse++
	case taken:
		hit.TakenTrue++
	default:
		hit.TakenFalse++
	}
}

// observeRuleCondition records the atoms of a guarded branch's condition, each
// evaluated on its own against the facts the occurrence published. compiled is
// the rule's own checked condition, or nil for a step guard compiled here.
func observeRuleCondition(ruleID, branchID, source string, compiled *oarcore.Rule, holder *evalHolder, gc *GuardContext, decision bool) {
	rec := ActiveRuleBranchRecorder.Load()
	if rec == nil || !rec.ObserveConditions || ruleID == "" || (gc != nil && gc.FromScenario) {
		return
	}
	if compiled == nil {
		env, err := capabilityEnvironment(InstalledEngineDeclaration())
		if err != nil {
			return
		}
		if compiled, err = oarcore.CompileCondition(source, env); err != nil {
			return
		}
	}
	holder.set(gc)
	values, err := compiled.EvaluateAtoms(activation(gc), specObservationFuncs(holder))
	if err != nil {
		return
	}
	atoms := make([]string, len(values))
	truths := make([]oarcore.Truth, len(values))
	known := make(map[string]bool, len(values))
	recomposes := true
	for i, v := range values {
		atoms[i], truths[i] = v.Atom, v.Value
		if v.Value == oarcore.Unknown {
			recomposes = false
		}
		known[v.Atom] = v.Value == oarcore.True
	}
	recomposes = recomposes && compiled.DecideFromAtoms(known) == decision
	partition := ""
	if rec.Partition != nil {
		partition = rec.Partition(gc)
	}
	rec.recordCondition(ruleID, branchID, source, compiled, atoms, truths, decision, recomposes, partition)
}

func (r *RuleBranchRecorder) recordCondition(ruleID, branchID, source string, compiled *oarcore.Rule, atoms []string, truths []oarcore.Truth, decision, recomposes bool, partition string) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.Conditions == nil {
		r.Conditions = make(map[string]*RuleConditionHit)
	}
	key := ruleID + "#" + branchID
	hit, ok := r.Conditions[key]
	if !ok {
		hit = &RuleConditionHit{RuleID: ruleID, BranchID: branchID, Source: source, Atoms: atoms, Decide: compiled.DecideFromAtoms}
		r.Conditions[key] = hit
	}
	for _, v := range hit.Vectors {
		if v.Decision == decision && v.Partition == partition && slices.Equal(v.Atoms, truths) {
			v.Count++
			return
		}
	}
	v := &RuleConditionVector{Atoms: truths, Decision: decision, Recomposes: recomposes, Partition: partition, Count: 1}
	if r.Label != nil {
		v.Label = r.Label()
	}
	hit.Vectors = append(hit.Vectors, v)
}
