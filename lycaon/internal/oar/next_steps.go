package oar

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// NextStepsFact is the host fact that carries an occurrence's reachable steps.
// It is computed only for a rule whose copy binds it, and no condition may read
// it, so it can change what a reader is told but never what a rule decides.
const NextStepsFact = "paintedwolf.next_steps"

// NextCallsDataKey holds the calls behind the rendered steps in decision data.
const NextCallsDataKey = api.ToolFeedbackNextCalls

const stepCallBinding = "call"

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
	// Pick maps dotted argument paths to list facts; the reader needs one entry.
	Pick map[string]string `yaml:"pick,omitempty"`
	// Complete says Args is the whole call rather than a shape to finish.
	Complete bool `yaml:"complete,omitempty"`
	// Say is the sentence shown when the call is reachable. It binds {{ call }}.
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
// that lets the fact reach a decision ([OAR-DOC-26] holds for the same reason).
func (l *Loader) validateNextSteps(r *Rule) error {
	if err := rejectDecisionNextSteps(r.ID, "when", r.When); err != nil {
		return err
	}
	for _, expr := range r.Flow {
		if err := rejectDecisionNextSteps(r.ID, "flow", expr); err != nil {
			return err
		}
	}
	if strings.TrimSpace(r.CounterScope) == NextStepsFact {
		return fmt.Errorf("rule %s: counter_scope reads %s, which only copy may bind", r.ID, NextStepsFact)
	}
	if r.NextSteps == nil {
		return nil
	}
	known := ruleFactRefs(r)
	for i, step := range r.NextSteps.Steps {
		if err := l.validateNextStep(step, known, fmt.Sprintf("rule %s next-steps[%d]", r.ID, i)); err != nil {
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

func (l *Loader) validateNextStep(step NextStep, known map[string]bool, where string) error {
	if strings.TrimSpace(step.Say) == "" {
		return fmt.Errorf("%s: say is required", where)
	}
	if !strings.Contains(step.Say, "{{ "+stepCallBinding+" }}") {
		return fmt.Errorf("%s: say must show {{ %s }}, the call the host checked", where, stepCallBinding)
	}
	if len(step.Program) > 0 {
		if step.Tool != "" || len(step.Args) > 0 || len(step.Pick) > 0 {
			return fmt.Errorf("%s: program stands alone; it cannot combine with tool, args or pick", where)
		}
		if !nativeProgramKnown(step.Program[0]) {
			return fmt.Errorf("%s: %q is not a native program", where, step.Program[0])
		}
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
		// A fact the rule does not reference is never assembled for it ([OAR-FACT-11]).
		if !known[name] {
			return fmt.Errorf("%s: names %s, which the rule does not reference", where, name)
		}
	}
	for path := range step.Pick {
		if _, dup := step.Args[path]; dup {
			return fmt.Errorf("%s: %s is set by both args and pick", where, path)
		}
	}
	for i, alternative := range step.Or {
		if err := l.validateNextStep(alternative, known, fmt.Sprintf("%s.or[%d]", where, i)); err != nil {
			return err
		}
	}
	return nil
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
	for _, value := range step.Args {
		if source, ok := value.(string); ok {
			templates = append(templates, source)
		}
	}
	for _, source := range templates {
		bindings, err := oarcore.ParseCopy(source)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if binding.Name != stepCallBinding {
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
	for _, name := range r.Requires.Facts {
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

// ResolvedSteps is one occurrence's reachable steps.
type ResolvedSteps struct {
	Text  string
	Calls []toolreach.Call
}

// ResolveNextSteps checks a rule's steps against the occurrence's frozen request.
// A step the request cannot reach is left out, or replaced by its Otherwise.
func ResolveNextSteps(r *Rule, gc *GuardContext) ResolvedSteps {
	var out ResolvedSteps
	if r == nil || r.NextSteps == nil || gc == nil {
		return out
	}
	facts := activation(gc)
	var sentences []string
	for _, step := range r.NextSteps.Steps {
		text, calls := resolveStep(step, gc, facts)
		if strings.TrimSpace(text) != "" {
			sentences = append(sentences, text)
		}
		out.Calls = append(out.Calls, calls...)
	}
	out.Text = strings.Join(sentences, " ")
	return out
}

func resolveStep(step NextStep, gc *GuardContext, facts map[string]any) (string, []toolreach.Call) {
	applies, err := EvaluateCondition(step.When, gc)
	if err != nil || !applies {
		return "", nil
	}
	if call, built := stepCall(step, gc, facts); built {
		if sequence, reachable := gc.Reach.Sequence(call); reachable {
			bound := make(map[string]any, len(facts)+1)
			for name, value := range facts {
				bound[name] = value
			}
			bound[stepCallBinding] = sequenceText(sequence)
			return oarcore.RenderCopy(step.Say, bound), sequence
		}
	}
	for _, alternative := range step.Or {
		if text, calls := resolveStep(alternative, gc, facts); strings.TrimSpace(text) != "" {
			return text, calls
		}
	}
	return oarcore.RenderCopy(step.Otherwise, facts), nil
}

func sequenceText(sequence []toolreach.Call) string {
	if len(sequence) == 1 {
		return sequence[0].Text()
	}
	return sequence[1].Text() + " after loading it with " + sequence[0].Text()
}

// stepCall builds the step's call. It reports false for a complete call or a
// program missing a value, which the reader could not submit as written.
func stepCall(step NextStep, gc *GuardContext, facts map[string]any) (toolreach.Call, bool) {
	if len(step.Program) > 0 {
		words := make([]string, len(step.Program))
		for i, source := range step.Program {
			words[i] = oarcore.RenderCopy(source, facts)
			if words[i] == "" {
				return toolreach.Call{}, false
			}
		}
		call := toolreach.ProgramCall(words...)
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
		if value := resolveStepArg(step.Args[path], facts); value != "" {
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

// resolveStepArg keeps a whole-value binding's type, so a list fact stays a list.
func resolveStepArg(value any, facts map[string]any) any {
	source, ok := value.(string)
	if !ok {
		return value
	}
	if bindings, err := oarcore.ParseCopy(source); err == nil && len(bindings) == 1 {
		if strings.TrimSpace(source) == "{{ "+bindings[0].Name+" }}" {
			if raw, present := facts[bindings[0].Name]; present {
				return raw
			}
		}
	}
	return oarcore.RenderCopy(source, facts)
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

// NextCallsDetails adds a decision's next calls to the details a refusal reports.
func NextCallsDetails(details, decisionData map[string]any) map[string]any {
	calls, present := decisionData[NextCallsDataKey]
	if !present {
		return details
	}
	merged := make(map[string]any, len(details)+1)
	for key, value := range details {
		merged[key] = value
	}
	merged[NextCallsDataKey] = calls
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
