package oar

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// NextStepsFact is the host fact that carries an occurrence's reachable steps.
// It is computed only for a rule whose copy binds it, and no condition may read
// it, so it can change what a reader is told but never what a rule decides.
const NextStepsFact = "paintedwolf.next_steps"

// stepCallBinding is the exact text of the step's own call.
const stepCallBinding = "call"

// stepLoadBinding is the exact loader call the reader must make before the
// step's call, or empty when the call's tool is already loaded.
const stepLoadBinding = "load"

// NextSteps is the host catalog entry for one rule. It lives outside the rule
// document, which stays portable Open Agent Rules.
type NextSteps struct {
	// Mentions lists tools the rule's copy names without asking for a call.
	Mentions []string   `yaml:"mentions,omitempty"`
	Steps    []NextStep `yaml:"steps,omitempty"`
}

// NextStep is one action a rule can offer.
type NextStep struct {
	// When limits the step to occurrences where this condition holds.
	When string `yaml:"when,omitempty"`
	// Tool names the call. Empty reissues the refused call.
	Tool string `yaml:"tool,omitempty"`
	// Args maps dotted argument paths to values. Strings are copy templates.
	Args map[string]any `yaml:"args,omitempty"`
	// Program is a native program as words, each a copy template. The host quotes
	// it and checks the program as well as the command tool.
	Program []string `yaml:"program,omitempty"`
	// Probe is the invocation the host checks for reach when Program shows the
	// reader placeholder words such as a line range. It names the same program.
	Probe []string `yaml:"probe,omitempty"`
	// Pick maps dotted argument paths to list facts; the reader needs one entry.
	Pick map[string]string `yaml:"pick,omitempty"`
	// Complete says Args is the whole call rather than a shape to finish.
	Complete bool `yaml:"complete,omitempty"`
	// Say is the sentence shown when the call is reachable. It binds {{ call }}
	// and, inside {% if load %}, the loader call {{ load }}.
	Say string `yaml:"say,omitempty"`
	// Otherwise is the sentence shown when When holds and the call is unreachable.
	Otherwise string `yaml:"otherwise,omitempty"`
	// Or lists steps to try in order when this step's call is unreachable.
	Or []NextStep `yaml:"or,omitempty"`
}

// DecodeNextSteps strictly decodes one catalog entry.
func DecodeNextSteps(body []byte) (*NextSteps, error) {
	var out NextSteps
	decoder := yaml.NewDecoder(strings.NewReader(string(body)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// bindsNextSteps reports whether a rule's copy displays the fact.
func bindsNextSteps(r *Rule) bool {
	for _, name := range copyRefs(r.Copy) {
		if name == NextStepsFact {
			return true
		}
	}
	return false
}

// validateNextSteps rejects a catalog entry that could not render, and any rule
// that lets the fact reach a decision ([OAR-DOC-20] holds for the same reason).
func (l *Loader) validateNextSteps(r *Rule) error {
	if err := rejectDecisionNextSteps(r.ID, "when", r.When); err != nil {
		return err
	}
	for _, expr := range r.Flow {
		if err := rejectDecisionNextSteps(r.ID, "flow", expr); err != nil {
			return err
		}
	}
	if r.NextSteps == nil {
		return nil
	}
	known := ruleFactRefs(r)
	selectors := r.Tools()
	for i, step := range r.NextSteps.Steps {
		if err := l.validateNextStep(step, known, selectors, fmt.Sprintf("rule %s next-steps[%d]", r.ID, i)); err != nil {
			return err
		}
	}
	return nil
}

func rejectDecisionNextSteps(ruleID, field, expr string) error {
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	refs, err := oarcore.ConditionReferences(expr)
	if err != nil {
		return err
	}
	for _, name := range refs {
		if name == NextStepsFact {
			return fmt.Errorf("rule %s: %s reads %s, which only copy may bind", ruleID, field, NextStepsFact)
		}
	}
	return nil
}

func (l *Loader) validateNextStep(step NextStep, known map[string]bool, selectors []string, where string) error {
	if err := toolcontract.RetiredUse(where, step.Tool); err != nil {
		return err
	}
	if strings.TrimSpace(step.Say) == "" {
		return fmt.Errorf("%s: say is required", where)
	}
	if err := checkStepSayBindings(step.Say, where); err != nil {
		return err
	}
	if len(step.Program) > 0 {
		if step.Tool != "" || len(step.Args) > 0 || len(step.Pick) > 0 {
			return fmt.Errorf("%s: program stands alone; it cannot combine with tool, args or pick", where)
		}
		if !nativeProgramKnown(step.Program[0]) {
			return fmt.Errorf("%s: %q is not a native program", where, step.Program[0])
		}
	}
	if len(step.Probe) > 0 && (len(step.Program) == 0 || step.Probe[0] != step.Program[0]) {
		return fmt.Errorf("%s: probe must check the step's own program", where)
	}
	if err := rejectDecisionNextSteps(where, "when", step.When); err != nil {
		return err
	}
	if err := l.checkWhen(step.When); err != nil {
		return fmt.Errorf("%s: when: %w", where, err)
	}
	names, err := nextStepRefs(step)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	for _, name := range names {
		// A fact the rule does not reference is never assembled for it ([OAR-FACT-9]).
		if !known[name] {
			return fmt.Errorf("%s: names %s, which the rule does not reference", where, name)
		}
	}
	for path := range step.Pick {
		if _, dup := step.Args[path]; dup {
			return fmt.Errorf("%s: %s is set by both args and pick", where, path)
		}
	}
	if err := l.checkStepArgPaths(step, selectors, where); err != nil {
		return err
	}
	for i, alternative := range step.Or {
		if err := l.validateNextStep(alternative, known, selectors, fmt.Sprintf("%s.or[%d]", where, i)); err != nil {
			return err
		}
	}
	return nil
}

// ErrStepLoadBranch reports a say that would not render true both for a reader
// who must load the call's tool first and for one who already has it.
var ErrStepLoadBranch = errors.New("say must show {{ load }} only inside {% if load %} and {{ call }} whether or not load is set")

// checkStepSayBindings holds say to both renderings: whether a call needs its
// loader depends on the reader, so it is known only at render time.
func checkStepSayBindings(say, where string) error {
	counts, err := oarcore.CountCopyBranches(say, stepLoadBinding)
	if err != nil {
		return fmt.Errorf("%s: say: %w", where, err)
	}
	call, load := counts[stepCallBinding], counts[stepLoadBinding]
	callShown := call.Either > 0 || (call.WhenSet > 0 && call.WhenEmpty > 0)
	if !callShown || load.WhenSet == 0 || load.WhenEmpty > 0 || load.Either > 0 {
		return fmt.Errorf("%s: %w", where, ErrStepLoadBranch)
	}
	return nil
}

// checkStepArgPaths pins every args and pick path to the argument schema of the
// tool that will receive it. Schemas the host did not load skip the check; the
// production catalog view always loads them.
func (l *Loader) checkStepArgPaths(step NextStep, selectors []string, where string) error {
	if l.toolSchemas == nil {
		return nil
	}
	paths := make([]string, 0, len(step.Args)+len(step.Pick))
	for path := range step.Args {
		paths = append(paths, path)
	}
	for path := range step.Pick {
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	if tool := strings.TrimSpace(step.Tool); tool != "" {
		return l.checkArgPaths(tool, paths, where)
	}
	// A reissue keeps the reader's tool: with selectors, every selected tool
	// must accept the shape; without them, some declared tool must.
	for _, path := range paths {
		if len(selectors) > 0 {
			for _, tool := range selectors {
				if err := l.checkArgPaths(tool, []string{path}, where); err != nil {
					return err
				}
			}
			continue
		}
		if !l.someToolAccepts(path) {
			return fmt.Errorf("%s: no declared tool accepts %s", where, path)
		}
	}
	return nil
}

func (l *Loader) checkArgPaths(tool string, paths []string, where string) error {
	meta, ok := l.toolSchemas.ToolMeta(tool)
	if !ok {
		return fmt.Errorf("%s: %s has no declared argument schema", where, tool)
	}
	accepted := map[string]bool{}
	for _, path := range toolschema.ArgFieldPaths(meta.ArgsSchema) {
		accepted[path] = true
	}
	for _, path := range paths {
		if !accepted[path] {
			return fmt.Errorf("%s: %s does not accept %s", where, tool, path)
		}
	}
	return nil
}

func (l *Loader) someToolAccepts(path string) bool {
	for name := range l.toolSchemas.Tools {
		meta, ok := l.toolSchemas.ToolMeta(name)
		if !ok {
			continue
		}
		for _, accepted := range toolschema.ArgFieldPaths(meta.ArgsSchema) {
			if accepted == path {
				return true
			}
		}
	}
	return false
}

func nativeProgramKnown(name string) bool {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return false
	}
	for _, spec := range catalog.Commands {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func nextStepRefs(step NextStep) ([]string, error) {
	var refs []string
	if when := strings.TrimSpace(step.When); when != "" {
		names, err := oarcore.ConditionReferences(when)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if _, fact := publishedFacts[name]; fact {
				refs = append(refs, name)
			}
		}
	}
	templates := append([]string{step.Say, step.Otherwise}, step.Program...)
	templates = append(templates, step.Probe...)
	for _, value := range step.Args {
		templates = collectArgTemplates(templates, value)
	}
	for _, source := range templates {
		bindings, err := oarcore.ParseCopy(source)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if binding.Name != stepCallBinding && binding.Name != stepLoadBinding {
				refs = append(refs, binding.Name)
			}
		}
	}
	for _, fact := range step.Pick {
		refs = append(refs, fact)
	}
	return refs, nil
}

// ruleFactRefs lists the facts a rule causes the engine to assemble.
func ruleFactRefs(r *Rule) map[string]bool {
	out := map[string]bool{"tool": true}
	for _, name := range r.RequiresFacts {
		out[name] = true
	}
	if names, err := oarcore.ConditionReferences(r.When); err == nil {
		for _, name := range names {
			out[name] = true
		}
	}
	for _, name := range copyRefs(r.Copy) {
		out[name] = true
	}
	return out
}

// StepResolutionOutcome describes how ResolveNextSteps resolved one step path.
type StepResolutionOutcome string

const (
	StepOutcomeEmittedCall      StepResolutionOutcome = "emitted_call"
	StepOutcomeEmittedOtherwise StepResolutionOutcome = "emitted_otherwise"
	StepOutcomeOmitted          StepResolutionOutcome = "omitted"
	StepOutcomeDisabled         StepResolutionOutcome = "disabled"
	StepOutcomeUnselected       StepResolutionOutcome = "unselected"
	// StepOutcomeFactsUnavailable is a step left out because its facts could not be assembled.
	StepOutcomeFactsUnavailable StepResolutionOutcome = "facts_unavailable"
)

// StepResolution records pre-concatenation provenance for one step or alternative.
type StepResolution struct {
	QualifiedRule string
	Anchor        string
	RuleID        string
	BranchPrefix  string
	HasGuard      bool
	GuardApplies  bool
	Reachable     bool
	Outcome       StepResolutionOutcome
	SelectedCalls []toolreach.Call
	RenderedText  string
}

// ResolvedSteps is one occurrence's reachable steps. Err joins the fact
// failures of steps that were omitted because their facts could not be assembled.
type ResolvedSteps struct {
	Text        string
	Calls       []toolreach.Call
	Resolutions []StepResolution
	Err         error
}

// assembleNextStepFacts gathers each step's facts and returns, keyed by branch
// prefix, the steps whose facts could not be assembled. A step with a missing
// fact would read it as absent, so it is omitted rather than resolved.
func assembleNextStepFacts(prefix string, steps []NextStep, gc *GuardContext, failed map[string]error) {
	for i, step := range steps {
		branch := fmt.Sprintf("%s[%d]", prefix, i)
		refs, err := nextStepRefs(step)
		if err == nil {
			err = AssembleFacts(gc, refs)
		}
		if err != nil {
			failed[branch] = fmt.Errorf("next step %s: %w", branch, err)
			continue
		}
		assembleNextStepFacts(branch+".or", step.Or, gc, failed)
	}
}

// ResolveNextSteps checks a rule's steps against the occurrence's frozen request.
// A step the request cannot reach is left out, or replaced by its Otherwise. A
// step whose facts could not be assembled is left out and reported in Err.
func ResolveNextSteps(ctx context.Context, r *Rule, gc *GuardContext) ResolvedSteps {
	var out ResolvedSteps
	if r == nil || r.NextSteps == nil || gc == nil {
		return out
	}
	var resolutions *[]StepResolution
	if hasActiveNextStepObserver(r, gc) {
		resolutions = &out.Resolutions
	}
	failed := map[string]error{}
	assembleNextStepFacts("steps", r.NextSteps.Steps, gc, failed)
	facts := activation(gc)
	resolver := stepResolver{rule: r, gc: gc, facts: facts, resolutions: resolutions, failed: failed}
	var sentences []string
	for i, step := range r.NextSteps.Steps {
		text, calls := resolver.resolve(ctx, fmt.Sprintf("steps[%d]", i), i, step)
		if strings.TrimSpace(text) != "" {
			sentences = append(sentences, text)
		}
		out.Calls = append(out.Calls, calls...)
	}
	out.Text = strings.Join(sentences, " ")
	out.Err = resolver.joinedFailures()
	if resolutions != nil {
		recordResolvedNextSteps(r, gc, out)
	}
	return out
}

func appendUnselectedAlternatives(r *Rule, parentPrefix string, startIdx int, alts []NextStep, resolutions *[]StepResolution) {
	ruleID := r.Qualified()
	for offset, alt := range alts {
		prefix := fmt.Sprintf("%s.or[%d]", parentPrefix, startIdx+offset)
		*resolutions = append(*resolutions, StepResolution{
			QualifiedRule: ruleID,
			Anchor:        r.Anchor,
			RuleID:        r.ID,
			BranchPrefix:  prefix,
			HasGuard:      strings.TrimSpace(alt.When) != "",
			Outcome:       StepOutcomeUnselected,
		})
		appendUnselectedAlternatives(r, prefix, 0, alt.Or, resolutions)
	}
}

// stepResolver resolves one rule's steps against assembled facts.
type stepResolver struct {
	rule        *Rule
	gc          *GuardContext
	facts       map[string]any
	resolutions *[]StepResolution
	failed      map[string]error
}

func (sr stepResolver) joinedFailures() error {
	if len(sr.failed) == 0 {
		return nil
	}
	branches := make([]string, 0, len(sr.failed))
	for branch := range sr.failed {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	errs := make([]error, len(branches))
	for i, branch := range branches {
		errs[i] = sr.failed[branch]
	}
	return errors.Join(errs...)
}

func (sr stepResolver) resolve(ctx context.Context, branchPrefix string, stepIndex int, step NextStep) (string, []toolreach.Call) {
	r, gc, facts, resolutions := sr.rule, sr.gc, sr.facts, sr.resolutions
	ruleID := r.Qualified()
	if _, unavailable := sr.failed[branchPrefix]; unavailable {
		if resolutions != nil {
			*resolutions = append(*resolutions, StepResolution{
				QualifiedRule: ruleID,
				Anchor:        r.Anchor,
				RuleID:        r.ID,
				BranchPrefix:  branchPrefix,
				HasGuard:      strings.TrimSpace(step.When) != "",
				Outcome:       StepOutcomeFactsUnavailable,
			})
			appendUnselectedAlternatives(r, branchPrefix, 0, step.Or, resolutions)
		}
		return "", nil
	}
	hasGuard := strings.TrimSpace(step.When) != ""
	applies, err := EvaluateCondition(step.When, gc)
	switch {
	case hasGuard && err == nil:
		recordRuleBranch(ruleID, branchPrefix+".when", stepIndex, gc, applies)
		observeRuleCondition(ruleID, branchPrefix+".when", step.When, nil, newEvalHolder(gc, nil, nil), gc, applies)
	case hasGuard:
		observeRuleError(ruleID, branchPrefix+".when", step.When, false, gc, err)
	}
	if err != nil || !applies {
		if resolutions != nil {
			*resolutions = append(*resolutions, StepResolution{
				QualifiedRule: ruleID,
				Anchor:        r.Anchor,
				RuleID:        r.ID,
				BranchPrefix:  branchPrefix,
				HasGuard:      hasGuard,
				Outcome:       StepOutcomeDisabled,
			})
			appendUnselectedAlternatives(r, branchPrefix, 0, step.Or, resolutions)
		}
		return "", nil
	}
	if call, built := stepCall(step, gc, facts); built {
		if sequence, reachable := gc.Reach.Sequence(ctx, call); reachable {
			recordRuleBranch(ruleID, branchPrefix+".reach", stepIndex, gc, true)
			bound := make(map[string]any, len(facts)+2)
			for name, value := range facts {
				bound[name] = value
			}
			bound[stepCallBinding], bound[stepLoadBinding] = sequenceBindings(sequence)
			say := oarcore.RenderCopy(step.Say, bound)
			if resolutions != nil {
				outcome := StepOutcomeOmitted
				var selected []toolreach.Call
				rendered := ""
				if strings.TrimSpace(say) != "" {
					outcome = StepOutcomeEmittedCall
					selected = slices.Clone(sequence)
					rendered = say
				}
				*resolutions = append(*resolutions, StepResolution{
					QualifiedRule: ruleID,
					Anchor:        r.Anchor,
					RuleID:        r.ID,
					BranchPrefix:  branchPrefix,
					HasGuard:      hasGuard,
					GuardApplies:  true,
					Reachable:     true,
					Outcome:       outcome,
					SelectedCalls: selected,
					RenderedText:  rendered,
				})
				appendUnselectedAlternatives(r, branchPrefix, 0, step.Or, resolutions)
			}
			return say, sequence
		}
	}
	recordRuleBranch(ruleID, branchPrefix+".reach", stepIndex, gc, false)
	parentIdx := -1
	if resolutions != nil {
		parentIdx = len(*resolutions)
		*resolutions = append(*resolutions, StepResolution{
			QualifiedRule: ruleID,
			Anchor:        r.Anchor,
			RuleID:        r.ID,
			BranchPrefix:  branchPrefix,
			HasGuard:      hasGuard,
			GuardApplies:  true,
			Outcome:       StepOutcomeOmitted,
		})
	}
	for j, alternative := range step.Or {
		if text, calls := sr.resolve(ctx, fmt.Sprintf("%s.or[%d]", branchPrefix, j), stepIndex, alternative); strings.TrimSpace(text) != "" {
			if resolutions != nil {
				appendUnselectedAlternatives(r, branchPrefix, j+1, step.Or[j+1:], resolutions)
			}
			return text, calls
		}
	}
	otherwise := oarcore.RenderCopy(step.Otherwise, facts)
	if resolutions != nil && strings.TrimSpace(otherwise) != "" {
		(*resolutions)[parentIdx].Outcome = StepOutcomeEmittedOtherwise
		(*resolutions)[parentIdx].RenderedText = otherwise
	}
	return otherwise, nil
}

// sequenceBindings returns the step's own call text and, when the reader must
// load its tool first, the loader call text.
func sequenceBindings(sequence []toolreach.Call) (call, load string) {
	call = sequence[len(sequence)-1].Text()
	if len(sequence) == 2 {
		load = sequence[0].Text()
	}
	return call, load
}

// stepCall builds the step's call. It reports false for a complete call or a
// program missing a value, which the reader could not submit as written.
func stepCall(step NextStep, gc *GuardContext, facts map[string]any) (toolreach.Call, bool) {
	if len(step.Program) > 0 {
		words, ok := renderProgramWords(step.Program, facts)
		if !ok {
			return toolreach.Call{}, false
		}
		call := toolreach.ProgramCall(words...)
		if len(step.Probe) > 0 {
			probe, ok := renderProgramWords(step.Probe, facts)
			if !ok {
				return toolreach.Call{}, false
			}
			call = call.WithProbe(probe...)
		}
		call.Partial = !step.Complete
		return call, true
	}
	call := toolreach.Call{Tool: step.Tool, Partial: !step.Complete}
	if call.Tool == "" {
		// A reissue keeps the reader's other arguments, which the host does not repeat.
		call.Tool, call.Partial = gc.Invocation.Tool, true
	}
	missing := false
	for _, path := range sortedStepPaths(step.Args) {
		if value, ok := resolveStepArg(step.Args[path], facts); ok {
			setStepArg(&call, path, value)
		} else {
			missing = true
		}
	}
	for _, path := range sortedStepPaths(step.Pick) {
		if value, ok := firstListValue(facts[step.Pick[path]]); ok {
			setStepArg(&call, path, value)
		} else {
			missing = true
		}
	}
	if missing {
		if step.Complete {
			return toolreach.Call{}, false
		}
		call.Partial = true
	}
	return call, true
}

// renderProgramWords renders each word; a word that renders empty leaves no program.
func renderProgramWords(sources []string, facts map[string]any) ([]string, bool) {
	words := make([]string, len(sources))
	for i, source := range sources {
		words[i] = oarcore.RenderCopy(source, facts)
		if words[i] == "" {
			return nil, false
		}
	}
	return words, true
}

// resolveStepArg keeps a whole-value binding's type and interpolates nested maps and slices.
func resolveStepArg(value any, facts map[string]any) (any, bool) {
	switch typed := value.(type) {
	case string:
		if bindings, err := oarcore.ParseCopy(typed); err == nil && len(bindings) == 1 {
			if strings.TrimSpace(typed) == "{{ "+bindings[0].Name+" }}" {
				raw, present := facts[bindings[0].Name]
				if !present || raw == "" {
					return nil, false
				}
				return raw, true
			}
		}
		rendered := oarcore.RenderCopy(typed, facts)
		return rendered, rendered != ""
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			resolved, ok := resolveStepArg(item, facts)
			if !ok {
				return nil, false
			}
			out[i] = resolved
		}
		return out, true
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, item := range typed {
			resolved, ok := resolveStepArg(item, facts)
			if !ok {
				return nil, false
			}
			out[k] = resolved
		}
		return out, true
	default:
		return value, value != nil
	}
}

func collectArgTemplates(dst []string, value any) []string {
	switch typed := value.(type) {
	case string:
		return append(dst, typed)
	case []any:
		for _, item := range typed {
			dst = collectArgTemplates(dst, item)
		}
	case map[string]any:
		for _, item := range typed {
			dst = collectArgTemplates(dst, item)
		}
	}
	return dst
}

func sortedStepPaths[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for path := range m {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func setStepArg(call *toolreach.Call, path string, value any) {
	if call.Args == nil {
		call.Args = map[string]any{}
	}
	conditions.DotPathSet(call.Args, path, value)
}

func firstListValue(v any) (any, bool) {
	switch list := v.(type) {
	case []string:
		if len(list) > 0 {
			return list[0], true
		}
	case []any:
		if len(list) > 0 {
			return list[0], true
		}
	}
	return nil, false
}

const (
	// PolicyRuleDetailKey names the qualified OAR rule ID attached to refusal feedback details.
	PolicyRuleDetailKey = "policy_rule"
	// PolicyAnchorDetailKey names the OAR anchor attached to refusal feedback details.
	PolicyAnchorDetailKey = "policy_anchor"
)

// NextCallsDetails adds a decision's next calls to the details a refusal reports.
func NextCallsDetails(details, decisionData map[string]any) map[string]any {
	calls, present := decisionData[api.ToolFeedbackNextCalls]
	if !present {
		return details
	}
	merged := make(map[string]any, len(details)+1)
	for key, value := range details {
		merged[key] = value
	}
	merged[api.ToolFeedbackNextCalls] = calls
	return merged
}

// PolicyDecisionDetails attaches policy_rule, policy_anchor, and next_calls to refusal details.
func PolicyDecisionDetails(anchor, rule string, details, decisionData map[string]any) map[string]any {
	base := NextCallsDetails(details, decisionData)
	anchor = strings.TrimSpace(anchor)
	rule = strings.TrimSpace(rule)
	if anchor == "" && rule == "" {
		return base
	}
	merged := make(map[string]any, len(base)+2)
	for key, value := range base {
		merged[key] = value
	}
	if rule != "" {
		merged[PolicyRuleDetailKey] = rule
	}
	if anchor != "" {
		merged[PolicyAnchorDetailKey] = anchor
	}
	return merged
}

// CanCallFacts lists the declared can_<tool> facts, which the frozen request answers.
func CanCallFacts() []string {
	var out []string
	for name := range bareFactTiers {
		if strings.HasPrefix(name, "can_") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
