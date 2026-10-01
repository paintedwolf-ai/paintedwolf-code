package oar

import (
	"fmt"
	"sort"
	"strings"
)

// Suppression — [OAR-EVAL-13] through [OAR-EVAL-18].
//
// A rule's `overrides` withdraw other rules for an occurrence. Load resolves every
// entry and refuses a set that cannot be evaluated deterministically; evaluation
// orders suppressors ahead of what they name, so a suppressed rule is never reached.

// ResolveOverrides validates and resolves every rule's overrides against the
// loaded set, filling each rule's resolved targets.
//
// [OAR-EVAL-13] an entry that resolves to no loaded rule is refused, naming it.
// [OAR-EVAL-17] an entry naming a mandatory rule is refused.
// [OAR-EVAL-16] a set whose overrides form a cycle is refused, naming the cycle.
func ResolveOverrides(rs *RuleSet) error {
	if rs == nil {
		return nil
	}
	all := rs.All()
	// Keyed by qualified identifier: the RuleSet's own index is bare-keyed, and
	// two publishers may ship the same bare id.
	byQualified := make(map[string]*Rule, len(all))
	for _, r := range all {
		byQualified[r.Qualified()] = r
	}
	for _, r := range all {
		if len(r.Overrides) == 0 {
			r.overrideTargets = nil
			continue
		}
		targets := make([]string, 0, len(r.Overrides))
		for _, entry := range r.Overrides {
			target, err := resolveOverrideEntry(byQualified, r, entry)
			if err != nil {
				return err
			}
			targets = append(targets, target)
		}
		r.overrideTargets = targets
	}
	return detectOverrideCycle(all)
}

// resolveOverrideEntry maps one entry to the qualified identifier of a loaded
// rule. An entry is a qualified identifier, or a bare id read in the suppressing
// rule's own namespace ([OAR-EVAL-13]).
func resolveOverrideEntry(byQualified map[string]*Rule, from *Rule, entry string) (string, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", fmt.Errorf("[OAR-EVAL-13] rule %q carries an empty overrides entry", from.Qualified())
	}
	want := entry
	if !strings.Contains(entry, "/") {
		want = RuleIdentity{Namespace: from.Namespace, ID: entry}.String()
	}
	target, ok := byQualified[want]
	if !ok {
		return "", fmt.Errorf(
			"[OAR-EVAL-13] rule %q overrides %s, which is not loaded",
			from.Qualified(), want)
	}
	if target.Mandatory {
		return "", fmt.Errorf(
			"[OAR-EVAL-17] rule %q overrides %s, whose mandatory is true",
			from.Qualified(), target.Qualified())
	}
	return target.Qualified(), nil
}

// detectOverrideCycle refuses a set whose overrides form a cycle ([OAR-EVAL-16]).
// Without this, [OAR-EVAL-18]'s ordering has no solution.
func detectOverrideCycle(all []*Rule) error {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(all))
	byQualified := make(map[string]*Rule, len(all))
	for _, r := range all {
		byQualified[r.Qualified()] = r
	}
	var stack []string
	var walk func(*Rule) error
	walk = func(r *Rule) error {
		switch state[r.Qualified()] {
		case done:
			return nil
		case onStack:
			at := 0
			for i, id := range stack {
				if id == r.Qualified() {
					at = i
					break
				}
			}
			return fmt.Errorf("[OAR-EVAL-16] overrides form a cycle: %s",
				strings.Join(append(append([]string(nil), stack[at:]...), r.Qualified()), " → "))
		}
		state[r.Qualified()] = onStack
		stack = append(stack, r.Qualified())
		for _, t := range r.overrideTargets {
			if next, ok := byQualified[t]; ok {
				if err := walk(next); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[r.Qualified()] = done
		return nil
	}
	ids := make([]string, 0, len(byQualified))
	for id := range byQualified {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := walk(byQualified[id]); err != nil {
			return err
		}
	}
	return nil
}

// orderBySuppression returns selected in the evaluation order [OAR-EVAL-18]
// mandates: while any selected rule remains unevaluated, take the one first in
// [OAR-EVAL-1] order among those none of whose remaining suppressors is still
// unevaluated. selected arrives already in [OAR-EVAL-1] order, so a single
// forward scan per round picks the right rule.
func orderBySuppression(selected []*Rule) []*Rule {
	suppressors := map[string][]string{} // rule id → selected rules naming it
	inSelection := make(map[string]bool, len(selected))
	for _, r := range selected {
		inSelection[r.Qualified()] = true
	}
	any := false
	for _, r := range selected {
		for _, t := range r.overrideTargets {
			if inSelection[t] {
				suppressors[t] = append(suppressors[t], r.Qualified())
				any = true
			}
		}
	}
	if !any {
		return selected
	}
	evaluated := make(map[string]bool, len(selected))
	out := make([]*Rule, 0, len(selected))
	remaining := append([]*Rule(nil), selected...)
	for len(remaining) > 0 {
		picked := -1
		for i, r := range remaining {
			ready := true
			for _, s := range suppressors[r.Qualified()] {
				if !evaluated[s] {
					ready = false
					break
				}
			}
			if ready {
				picked = i
				break
			}
		}
		if picked < 0 {
			// Unreachable while [OAR-EVAL-16] holds; degrade to the base order
			// rather than dropping rules from the occurrence.
			out = append(out, remaining...)
			return out
		}
		r := remaining[picked]
		evaluated[r.Qualified()] = true
		out = append(out, r)
		remaining = append(remaining[:picked], remaining[picked+1:]...)
	}
	return out
}

// traceSuppressedRemainder records the rules a firing suppressor withdrew that a
// short-circuit then skipped past ([OAR-OPS-10]). Order follows the evaluation
// order the occurrence was already using.
func traceSuppressedRemainder(res *PipelineResult, ordered []*Rule, from int, suppressed map[string]bool, stage Stage) {
	for _, r := range ordered[from:] {
		if suppressed[r.Qualified()] {
			res.Trace.Add(TraceEntry{Rule: r.Qualified(), Stage: stage, Outcome: TraceSuppressed})
		}
	}
}
