package oar

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// ResolvedRemedy is one remedy bound to an occurrence's facts.
type ResolvedRemedy struct {
	// Tool is empty when the remedy reissues the refused call.
	Tool    string
	Args    map[string]any
	Outcome string
}

// validateRemedies checks the extension without touching the compiled rule, so
// a rule decides identically with or without it ([OAR-DOC-26]).
func (l *Loader) validateRemedies(r *Rule) error {
	if len(r.Remedies) == 0 {
		return nil
	}
	known := ruleFactRefs(r)
	for i, remedy := range r.Remedies {
		where := fmt.Sprintf("rule %s x-paintedwolf-remedies[%d]", r.ID, i)
		switch remedy.Outcome {
		case "", guidance.RemedyClears, guidance.RemedyObserves:
		default:
			return fmt.Errorf("%s: outcome %q is neither %s nor %s", where, remedy.Outcome, guidance.RemedyClears, guidance.RemedyObserves)
		}
		if err := l.checkWhen(remedy.When); err != nil {
			return fmt.Errorf("%s: when: %w", where, err)
		}
		refs, err := remedyRefs(remedy)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		for _, name := range refs {
			// Producing a fact only for this extension would change what the
			// engine assembles for the rule ([OAR-FACT-11]).
			if !known[name] {
				return fmt.Errorf("%s: names %s, which the rule does not reference", where, name)
			}
		}
		for path := range remedy.Pick {
			if _, dup := remedy.Args[path]; dup {
				return fmt.Errorf("%s: %s is set by both args and pick", where, path)
			}
		}
	}
	return nil
}

func remedyRefs(remedy guidance.RemedyEntry) ([]string, error) {
	var refs []string
	if when := strings.TrimSpace(remedy.When); when != "" {
		names, err := oarcore.ConditionReferences(when)
		if err != nil {
			return nil, err
		}
		refs = append(refs, names...)
	}
	for path, source := range remedy.Args {
		bindings, err := oarcore.ParseCopy(source)
		if err != nil {
			return nil, fmt.Errorf("args.%s: %w", path, err)
		}
		for _, b := range bindings {
			refs = append(refs, b.Name)
		}
	}
	for _, fact := range remedy.Pick {
		refs = append(refs, fact)
	}
	return refs, nil
}

// ruleFactRefs lists the facts a rule already causes the engine to assemble.
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

// ResolveRemedies binds a rule's remedies to one occurrence. It runs after the
// decision and only reads, so it cannot change what the rule decided.
func ResolveRemedies(r *Rule, gc *GuardContext) ([]ResolvedRemedy, error) {
	if r == nil || len(r.Remedies) == 0 {
		return nil, nil
	}
	facts := activation(gc)
	var out []ResolvedRemedy
	for _, remedy := range r.Remedies {
		applies, err := EvaluateCondition(remedy.When, gc)
		if err != nil {
			return nil, fmt.Errorf("rule %s remedy %q: %w", r.ID, remedy.Tool, err)
		}
		if !applies {
			continue
		}
		outcome := remedy.Outcome
		if outcome == "" {
			outcome = guidance.RemedyClears
		}
		resolved := ResolvedRemedy{Tool: remedy.Tool, Outcome: outcome}
		for _, path := range sortedRemedyPaths(remedy.Args) {
			setArgPath(&resolved.Args, path, oarcore.RenderCopy(remedy.Args[path], facts))
		}
		for _, path := range sortedRemedyPaths(remedy.Pick) {
			if value, ok := firstListValue(facts[remedy.Pick[path]]); ok {
				setArgPath(&resolved.Args, path, value)
			}
		}
		out = append(out, resolved)
	}
	return out, nil
}

func sortedRemedyPaths(m map[string]string) []string {
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
