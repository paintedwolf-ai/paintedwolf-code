package hoststate

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pongoplain"
)

// ExpectedZeroSide identifies a boolean branch outcome excluded by a structural production invariant.
type ExpectedZeroSide string

const (
	ExpectedZeroNone  ExpectedZeroSide = ""
	ExpectedZeroTrue  ExpectedZeroSide = "true"
	ExpectedZeroFalse ExpectedZeroSide = "false"
)

// ExpectedZeroBranch documents one branch outcome that is structurally unreachable under production constraints.
type ExpectedZeroBranch struct {
	ID     string
	Side   ExpectedZeroSide
	Reason string
}

// TemplateBranchSummary reports static discovery and runtime hits for one template branch.
type TemplateBranchSummary struct {
	Template           string
	BranchID           string
	Index              int
	Line               int
	Col                int
	Kind               pongoplain.BranchKind
	Expression         string
	TakenTrue          int
	TakenFalse         int
	ExpectedZero       ExpectedZeroSide
	ExpectedZeroReason string
}

// Dead reports whether neither outcome was ever reached.
func (b TemplateBranchSummary) Dead() bool { return b.TakenTrue == 0 && b.TakenFalse == 0 }

// Vacuous reports whether only one boolean outcome was ever reached without a grounded ExpectedZero exclusion.
// Loop headers without an {% empty %} clause (BranchFor) are checked for
// non-zero execution via Dead(); {% empty %} clauses are tracked as BranchEmpty.
func (b TemplateBranchSummary) Vacuous() bool {
	if b.Kind == pongoplain.BranchFor || b.Dead() {
		return false
	}
	switch b.ExpectedZero {
	case ExpectedZeroTrue:
		return b.TakenTrue != 0 || b.TakenFalse == 0 || strings.TrimSpace(b.ExpectedZeroReason) == ""
	case ExpectedZeroFalse:
		return b.TakenFalse != 0 || b.TakenTrue == 0 || strings.TrimSpace(b.ExpectedZeroReason) == ""
	default:
		return b.TakenTrue == 0 || b.TakenFalse == 0
	}
}

// RuleBranchSummary reports static discovery and witness outcomes for one OAR rule/step branch.
type RuleBranchSummary struct {
	RuleID             string
	BranchID           string
	StepIndex          int
	TakenTrue          int
	TakenFalse         int
	ScenarioTrue       int
	ScenarioFalse      int
	ExpectedZero       ExpectedZeroSide
	ExpectedZeroReason string
}

// Dead reports whether no production GuardContext witness ever evaluated this branch.
func (b RuleBranchSummary) Dead() bool { return b.TakenTrue == 0 && b.TakenFalse == 0 }

// Vacuous reports whether production GuardContext witnesses reached only one
// outcome without a grounded ExpectedZero exclusion.
func (b RuleBranchSummary) Vacuous() bool {
	if b.Dead() {
		return false
	}
	switch b.ExpectedZero {
	case ExpectedZeroTrue:
		return b.TakenTrue != 0 || b.TakenFalse == 0 || strings.TrimSpace(b.ExpectedZeroReason) == ""
	case ExpectedZeroFalse:
		return b.TakenFalse != 0 || b.TakenTrue == 0 || strings.TrimSpace(b.ExpectedZeroReason) == ""
	default:
		return b.TakenTrue == 0 || b.TakenFalse == 0
	}
}

// CoverageReport summarizes discovered artifacts, template and OAR branches,
// reached cells, and excluded cells with production exclusion reasons.
type CoverageReport struct {
	DiscoveredTemplates []string
	DiscoveredRules     []string
	TemplateBranches    []TemplateBranchSummary
	RuleBranches        []RuleBranchSummary
	ExpectedZero        []ExpectedZeroBranch
	// UnattributedRefs are composed template refs with recorded branches that
	// name no registered unit.
	UnattributedRefs []string
	ReachedCells     int
	ExcludedCells    int
	ExcludedByReason map[string]int
}

// DeadTemplateBranches returns all template branches with 0 runtime evaluations.
func (r CoverageReport) DeadTemplateBranches() []TemplateBranchSummary {
	var out []TemplateBranchSummary
	for _, b := range r.TemplateBranches {
		if b.Dead() {
			out = append(out, b)
		}
	}
	return out
}

// VacuousTemplateBranches returns all template branches with only one boolean outcome.
func (r CoverageReport) VacuousTemplateBranches() []TemplateBranchSummary {
	var out []TemplateBranchSummary
	for _, b := range r.TemplateBranches {
		if b.Vacuous() {
			out = append(out, b)
		}
	}
	return out
}

// DeadRuleBranches returns all OAR rule/step branches with 0 non-scenario evaluations.
func (r CoverageReport) DeadRuleBranches() []RuleBranchSummary {
	var out []RuleBranchSummary
	for _, b := range r.RuleBranches {
		if b.Dead() {
			out = append(out, b)
		}
	}
	return out
}

// VacuousRuleBranches returns all OAR rule/step branches with only one non-scenario outcome.
func (r CoverageReport) VacuousRuleBranches() []RuleBranchSummary {
	var out []RuleBranchSummary
	for _, b := range r.RuleBranches {
		if b.Vacuous() {
			out = append(out, b)
		}
	}
	return out
}

// Validate fails when any discovered template or OAR branch is dead or vacuous.
func (r CoverageReport) Validate() error {
	var issues []string
	for _, b := range r.DeadTemplateBranches() {
		issues = append(issues, fmt.Sprintf("dead template branch %s", b.BranchID))
	}
	for _, b := range r.VacuousTemplateBranches() {
		issues = append(issues, fmt.Sprintf("vacuous template branch %s (true=%d false=%d)", b.BranchID, b.TakenTrue, b.TakenFalse))
	}
	for _, b := range r.DeadRuleBranches() {
		issues = append(issues, fmt.Sprintf("dead OAR branch %s#%s (scenario true=%d false=%d)", b.RuleID, b.BranchID, b.ScenarioTrue, b.ScenarioFalse))
	}
	for _, b := range r.VacuousRuleBranches() {
		issues = append(issues, fmt.Sprintf("vacuous OAR branch %s#%s (true=%d false=%d)", b.RuleID, b.BranchID, b.TakenTrue, b.TakenFalse))
	}
	for _, ref := range r.UnattributedRefs {
		issues = append(issues, fmt.Sprintf("rendered template %s names no registered unit", ref))
	}
	if len(issues) > 0 {
		return fmt.Errorf("branch coverage gaps (%d):\n%s", len(issues), strings.Join(issues, "\n"))
	}
	return nil
}

type discoveredRuleBranch struct {
	ruleID    string
	branchID  string
	stepIndex int
}

// CoverageTracker coordinates opt-in pongoplain and OAR branch recording across a test run.
type CoverageTracker struct {
	mu                sync.Mutex
	templateRec       *pongoplain.BranchRecorder
	ruleRec           *oar.RuleBranchRecorder
	templateSpecs     map[string][]pongoplain.BranchSpec
	inlineOwners      map[string]string
	ruleSpecs         map[string][]discoveredRuleBranch
	expectedZeroTpl   map[string]ExpectedZeroBranch
	expectedZeroRules map[string]ExpectedZeroBranch
	resolveRef        func(string) (string, bool)
	reachedCells      int
	excludedCells     int
	excludedWhy       map[string]int
}

// NewCoverageTracker creates a branch and cell coverage tracker ready to install.
func NewCoverageTracker() *CoverageTracker {
	return &CoverageTracker{
		templateRec:       &pongoplain.BranchRecorder{Aliases: map[string]string{}, Hits: map[string]*pongoplain.BranchHit{}},
		ruleRec:           &oar.RuleBranchRecorder{Hits: map[string]*oar.RuleBranchHit{}},
		templateSpecs:     make(map[string][]pongoplain.BranchSpec),
		inlineOwners:      make(map[string]string),
		ruleSpecs:         make(map[string][]discoveredRuleBranch),
		expectedZeroTpl:   make(map[string]ExpectedZeroBranch),
		expectedZeroRules: make(map[string]ExpectedZeroBranch),
		excludedWhy:       make(map[string]int),
	}
}

// ExpectZeroTemplateBranch records a grounded structural constraint keeping one side of branchID at 0.
func (t *CoverageTracker) ExpectZeroTemplateBranch(branchID string, side ExpectedZeroSide, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expectedZeroTpl[branchID] = ExpectedZeroBranch{ID: branchID, Side: side, Reason: reason}
}

// ExpectZeroRuleBranch records a grounded structural constraint keeping one side of ruleID#branchID at 0.
func (t *CoverageTracker) ExpectZeroRuleBranch(ruleID, branchID string, side ExpectedZeroSide, reason string) {
	key := ruleID + "#" + branchID
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expectedZeroRules[key] = ExpectedZeroBranch{ID: key, Side: side, Reason: reason}
}

// Install activates both pongoplain and OAR branch recorders and returns a restore func.
func (t *CoverageTracker) Install() func() {
	prevTpl := pongoplain.ActiveBranchRecorder.Swap(t.templateRec)
	prevRule := oar.ActiveRuleBranchRecorder.Swap(t.ruleRec)
	return func() {
		pongoplain.ActiveBranchRecorder.Store(prevTpl)
		oar.ActiveRuleBranchRecorder.Store(prevRule)
	}
}

// RegisterTemplate discovers static branches in source and aliases inline renders to unitID.
func (t *CoverageTracker) RegisterTemplate(unitID, source string, profile pongoplain.Profile) error {
	analysis, err := pongoplain.Inspect(source, profile)
	if err != nil {
		return fmt.Errorf("inspect template %s: %w", unitID, err)
	}
	inlineID := pongoplain.InlineTemplateID(source)
	t.templateRec.Mu.Lock()
	if t.templateRec.Aliases == nil {
		t.templateRec.Aliases = make(map[string]string)
	}
	owner, exists := t.templateRec.Aliases[inlineID]
	if !exists {
		owner = unitID
		t.templateRec.Aliases[inlineID] = unitID
	}
	t.templateRec.Mu.Unlock()

	t.mu.Lock()
	t.templateSpecs[unitID] = analysis.Branches
	t.inlineOwners[unitID] = owner
	t.mu.Unlock()
	return nil
}

// AttributeTemplateRefs names the registered unit a composed render's
// template ref reads, so its hits attribute to that unit.
func (t *CoverageTracker) AttributeTemplateRefs(resolve func(ref string) (string, bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolveRef = resolve
}

// RegisterRule discovers conditional when and next-steps branches on rule.
func (t *CoverageTracker) RegisterRule(rule *oar.Rule) {
	if rule == nil {
		return
	}
	id := rule.Qualified()
	var specs []discoveredRuleBranch
	if strings.TrimSpace(rule.When) != "" {
		specs = append(specs, discoveredRuleBranch{ruleID: id, branchID: "when", stepIndex: -1})
	}
	if rule.NextSteps != nil {
		for i, step := range rule.NextSteps.Steps {
			specs = appendStepBranches(specs, id, fmt.Sprintf("steps[%d]", i), i, step)
		}
	}
	t.mu.Lock()
	t.ruleSpecs[id] = specs
	t.mu.Unlock()
}

func appendStepBranches(out []discoveredRuleBranch, ruleID, prefix string, stepIdx int, step oar.NextStep) []discoveredRuleBranch {
	if strings.TrimSpace(step.When) != "" {
		out = append(out, discoveredRuleBranch{ruleID: ruleID, branchID: prefix + ".when", stepIndex: stepIdx})
	}
	out = append(out, discoveredRuleBranch{ruleID: ruleID, branchID: prefix + ".reach", stepIndex: stepIdx})
	for j, alt := range step.Or {
		out = appendStepBranches(out, ruleID, fmt.Sprintf("%s.or[%d]", prefix, j), stepIdx, alt)
	}
	return out
}

// RecordCell tallies a reached or excluded state cell.
func (t *CoverageTracker) RecordCell(excluded bool, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if excluded {
		t.excludedCells++
		t.excludedWhy[reason]++
		return
	}
	t.reachedCells++
}

// Report snapshots discovered artifacts, branch counts, and state-cell totals.
func (t *CoverageTracker) Report() CoverageReport {
	t.mu.Lock()
	tplIDs := sortedMapKeys(t.templateSpecs)
	ruleIDs := sortedMapKeys(t.ruleSpecs)
	reached := t.reachedCells
	excluded := t.excludedCells
	reasons := make(map[string]int, len(t.excludedWhy))
	for k, v := range t.excludedWhy {
		reasons[k] = v
	}
	tplSpecsCopy := make(map[string][]pongoplain.BranchSpec, len(t.templateSpecs))
	for k, v := range t.templateSpecs {
		tplSpecsCopy[k] = slices.Clone(v)
	}
	inlineOwnersCopy := make(map[string]string, len(t.inlineOwners))
	for k, v := range t.inlineOwners {
		inlineOwnersCopy[k] = v
	}
	ruleSpecsCopy := make(map[string][]discoveredRuleBranch, len(t.ruleSpecs))
	for k, v := range t.ruleSpecs {
		ruleSpecsCopy[k] = slices.Clone(v)
	}
	expTplCopy := make(map[string]ExpectedZeroBranch, len(t.expectedZeroTpl))
	for k, v := range t.expectedZeroTpl {
		expTplCopy[k] = v
	}
	expRuleCopy := make(map[string]ExpectedZeroBranch, len(t.expectedZeroRules))
	for k, v := range t.expectedZeroRules {
		expRuleCopy[k] = v
	}
	resolve := t.resolveRef
	t.mu.Unlock()
	hits, unattributed := t.attributedHits(tplSpecsCopy, resolve)

	var allExp []ExpectedZeroBranch
	for _, k := range sortedMapKeys(expTplCopy) {
		allExp = append(allExp, expTplCopy[k])
	}
	for _, k := range sortedMapKeys(expRuleCopy) {
		allExp = append(allExp, expRuleCopy[k])
	}

	return CoverageReport{
		DiscoveredTemplates: tplIDs,
		DiscoveredRules:     ruleIDs,
		TemplateBranches:    collectTemplateSummaries(tplIDs, tplSpecsCopy, inlineOwnersCopy, expTplCopy, hits),
		RuleBranches:        t.collectRuleSummaries(ruleIDs, ruleSpecsCopy, expRuleCopy),
		ExpectedZero:        allExp,
		UnattributedRefs:    unattributed,
		ReachedCells:        reached,
		ExcludedCells:       excluded,
		ExcludedByReason:    reasons,
	}
}

// attributedHits keys every recorded template hit by its registered unit:
// directly, through an inline alias applied at record time, or through the
// production ref resolution. Hits of one unit read through several refs add up.
func (t *CoverageTracker) attributedHits(specs map[string][]pongoplain.BranchSpec, resolve func(string) (string, bool)) (map[string][2]int, []string) {
	t.templateRec.Mu.Lock()
	defer t.templateRec.Mu.Unlock()
	out := make(map[string][2]int, len(t.templateRec.Hits))
	missing := map[string]bool{}
	for _, hit := range t.templateRec.Hits {
		unit, ok := attributeTemplate(hit.Template, specs, resolve)
		if !ok {
			if !strings.HasPrefix(hit.Template, "inline:") {
				missing[hit.Template] = true
			}
			continue
		}
		key := fmt.Sprintf("%s@L%d:C%d:%s", unit, hit.Line, hit.Col, hit.Kind)
		cur := out[key]
		out[key] = [2]int{cur[0] + hit.TakenTrue, cur[1] + hit.TakenFalse}
	}
	return out, sortedMapKeys(missing)
}

func attributeTemplate(name string, specs map[string][]pongoplain.BranchSpec, resolve func(string) (string, bool)) (string, bool) {
	if _, ok := specs[name]; ok {
		return name, true
	}
	if resolve == nil {
		return "", false
	}
	unit, ok := resolve(name)
	if !ok {
		return "", false
	}
	_, registered := specs[unit]
	return unit, registered
}

func collectTemplateSummaries(
	tplIDs []string,
	specs map[string][]pongoplain.BranchSpec,
	inlineOwners map[string]string,
	expZero map[string]ExpectedZeroBranch,
	hits map[string][2]int,
) []TemplateBranchSummary {
	var out []TemplateBranchSummary
	for _, unitID := range tplIDs {
		owner := inlineOwners[unitID]
		for _, sp := range specs[unitID] {
			suffix := fmt.Sprintf("@L%d:C%d:%s", sp.Line, sp.Col, sp.Kind)
			branchID := unitID + suffix
			taken, ok := hits[branchID]
			if !ok && owner != "" && owner != unitID {
				taken = hits[owner+suffix]
			}
			ez := expZero[branchID]
			out = append(out, TemplateBranchSummary{
				Template:           unitID,
				BranchID:           branchID,
				Index:              sp.Index,
				Line:               sp.Line,
				Col:                sp.Col,
				Kind:               sp.Kind,
				Expression:         sp.Expression,
				TakenTrue:          taken[0],
				TakenFalse:         taken[1],
				ExpectedZero:       ez.Side,
				ExpectedZeroReason: ez.Reason,
			})
		}
	}
	return out
}

func (t *CoverageTracker) collectRuleSummaries(
	ruleIDs []string,
	specs map[string][]discoveredRuleBranch,
	expZero map[string]ExpectedZeroBranch,
) []RuleBranchSummary {
	t.ruleRec.Mu.Lock()
	defer t.ruleRec.Mu.Unlock()
	var out []RuleBranchSummary
	for _, ruleID := range ruleIDs {
		for _, sp := range specs[ruleID] {
			key := sp.ruleID + "#" + sp.branchID
			ez := expZero[key]
			row := RuleBranchSummary{
				RuleID:             sp.ruleID,
				BranchID:           sp.branchID,
				StepIndex:          sp.stepIndex,
				ExpectedZero:       ez.Side,
				ExpectedZeroReason: ez.Reason,
			}
			if hit := t.ruleRec.Hits[key]; hit != nil {
				row.TakenTrue = hit.TakenTrue
				row.TakenFalse = hit.TakenFalse
				row.ScenarioTrue = hit.ScenarioTrue
				row.ScenarioFalse = hit.ScenarioFalse
			}
			out = append(out, row)
		}
	}
	return out
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
