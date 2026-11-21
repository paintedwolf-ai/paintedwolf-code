package oar

import (
	"sync"
	"sync/atomic"
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

// RuleBranchRecorder collects OAR rule when and next-steps branch outcomes when
// installed on ActiveRuleBranchRecorder (zero cost when nil).
type RuleBranchRecorder struct {
	Mu   sync.Mutex
	Hits map[string]*RuleBranchHit
}

// ActiveRuleBranchRecorder is the opt-in recorder consulted by rule and next-step evaluation.
var ActiveRuleBranchRecorder atomic.Pointer[RuleBranchRecorder]

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
