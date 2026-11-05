package oar

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/toolreach"
	"gopkg.in/yaml.v3"
)

// NextStepsFact is the host fact that carries an occurrence's reachable steps.
// It is computed only for a rule whose copy binds it, and no condition may read
// it, so it can change what a reader is told but never what a rule decides.
const NextStepsFact = "paintedwolf.next_steps"

// NextCallsDataKey holds the calls behind the rendered steps in decision data.
const NextCallsDataKey = "next_calls"

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
	// Pick maps dotted argument paths to list facts; the reader needs one entry.
	Pick map[string]string `yaml:"pick,omitempty"`
	// Complete says Args is the whole call rather than a shape to finish.
	Complete bool `yaml:"complete,omitempty"`
	// Say is the sentence shown when the call is reachable. It binds {{ call }}.
	Say string `yaml:"say,omitempty"`
	// Otherwise is the sentence shown when When holds and the call is unreachable.
	Otherwise string `yaml:"otherwise,omitempty"`
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
	if strings.TrimSpace(r.When) != "" {
		refs, err := oarcore.ConditionReferences(r.When)
		if err != nil {
			return err
		}
		for _, name := range refs {
			if name == NextStepsFact {
				return fmt.Errorf("rule %s: when reads %s, which only copy may bind", r.ID, NextStepsFact)
			}
		}
	}
	if r.NextSteps == nil {
		return nil
	}
	known := ruleFactRefs(r)
	for i, step := range r.NextSteps.Steps {
		where := fmt.Sprintf("rule %s next-steps[%d]", r.ID, i)
		if strings.TrimSpace(step.Say) == "" {
			return fmt.Errorf("%s: say is required", where)
		}
		if !strings.Contains(step.Say, "{{ "+stepCallBinding+" }}") {
			return fmt.Errorf("%s: say must show {{ %s }}, the call the host checked", where, stepCallBinding)
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
	}
	return nil
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
	templates := []string{step.Say, step.Otherwise}
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
		applies, err := EvaluateCondition(step.When, gc)
		if err != nil || !applies {
			continue
		}
		call := stepCall(step, gc, facts)
		sequence, reachable := gc.Reach.Sequence(call)
		if !reachable {
			if text := oarcore.RenderCopy(step.Otherwise, facts); strings.TrimSpace(text) != "" {
				sentences = append(sentences, text)
			}
			continue
		}
		out.Calls = append(out.Calls, sequence...)
		bound := make(map[string]any, len(facts)+1)
		for name, value := range facts {
			bound[name] = value
		}
		bound[stepCallBinding] = sequenceText(sequence)
		sentences = append(sentences, oarcore.RenderCopy(step.Say, bound))
	}
	out.Text = strings.Join(sentences, " ")
	return out
}

func sequenceText(sequence []toolreach.Call) string {
	if len(sequence) == 1 {
		return sequence[0].Text()
	}
	return sequence[1].Text() + " after loading it with " + sequence[0].Text()
}

func stepCall(step NextStep, gc *GuardContext, facts map[string]any) toolreach.Call {
	call := toolreach.Call{Tool: step.Tool, Partial: !step.Complete}
	if call.Tool == "" {
		// A reissue keeps the reader's other arguments, which the host does not repeat.
		call.Tool, call.Partial = gc.Invocation.Tool, true
	}
	for _, path := range sortedStepPaths(step.Args) {
		setArgPath(&call.Args, path, resolveStepArg(step.Args[path], facts))
	}
	for _, path := range sortedStepPaths(step.Pick) {
		if value, ok := firstListValue(facts[step.Pick[path]]); ok {
			setArgPath(&call.Args, path, value)
		}
	}
	return call
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

func setArgPath(args *map[string]any, path string, value any) {
	if *args == nil {
		*args = map[string]any{}
	}
	cur := *args
	segments := strings.Split(path, ".")
	for _, segment := range segments[:len(segments)-1] {
		next, ok := cur[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[segment] = next
		}
		cur = next
	}
	cur[segments[len(segments)-1]] = value
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
