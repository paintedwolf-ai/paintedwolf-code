package toolvocab

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

// invalidRemedies checks that declared remedies are callable as written and that
// the copy and the declaration name the same calls. A portable rule without the
// extension has nothing to check.
func invalidRemedies(c *Catalog, rule *oar.Rule) []string {
	if len(rule.Remedies) == 0 && len(rule.Mentions) == 0 {
		return nil
	}
	var problems []string
	selected := rule.Selector["tool"]
	for _, remedy := range rule.Remedies {
		tool := strings.TrimSpace(remedy.Tool)
		targets := []string{tool}
		if tool == "" {
			if len(selected) == 0 {
				problems = append(problems, fmt.Sprintf(
					"rule %q: a remedy without a tool reissues the refused call, but the rule selects no tool", rule.ID))
				continue
			}
			targets = selected
		} else if !c.KnownName(tool) {
			problems = append(problems, fmt.Sprintf("rule %q: remedy names %q, which is not a registered tool", rule.ID, tool))
			continue
		}
		for _, path := range remedyArgPaths(remedy) {
			for _, target := range targets {
				if !c.acceptsArg(target, path) {
					problems = append(problems, fmt.Sprintf("rule %q: remedy sets %s, which %q does not accept", rule.ID, path, target))
				}
			}
		}
	}
	for _, tool := range rule.Mentions {
		if !c.KnownName(tool) {
			problems = append(problems, fmt.Sprintf("rule %q: x-paintedwolf-mentions names %q, which is not a registered tool", rule.ID, tool))
		}
	}
	return append(problems, remedyCopyMismatches(c, rule)...)
}

// remedyCopyMismatches compares the calls fix and instead name with the
// declaration, in both directions.
func remedyCopyMismatches(c *Catalog, rule *oar.Rule) []string {
	var problems []string
	for _, tool := range UndeclaredRemedies(c, rule) {
		problems = append(problems, fmt.Sprintf(
			"rule %q: copy names %q, which is neither a declared remedy nor a declared mention", rule.ID, tool))
	}
	named := remedyCopyTools(c, rule)
	for _, remedy := range rule.Remedies {
		tool := strings.TrimSpace(remedy.Tool)
		if tool != "" && !slices.Contains(named, tool) {
			problems = append(problems, fmt.Sprintf("rule %q: remedy %q is declared, but copy never asks for it", rule.ID, tool))
		}
	}
	return problems
}

func remedyCopyTools(c *Catalog, rule *oar.Rule) []string {
	return AnyNamedTools(c, rule.Fix+"\n"+rule.Copy.Instead)
}

// UndeclaredRemedies names the tools a rule's copy asks for without declaring
// them. Stock rules declare every call; portable rules may carry no extension.
func UndeclaredRemedies(c *Catalog, rule *oar.Rule) []string {
	if rule == nil {
		return nil
	}
	declared := map[string]bool{}
	for _, remedy := range rule.Remedies {
		declared[strings.TrimSpace(remedy.Tool)] = true
	}
	var out []string
	for _, tool := range remedyCopyTools(c, rule) {
		if !declared[tool] && !slices.Contains(rule.Mentions, tool) && !slices.Contains(rule.Selector["tool"], tool) {
			out = append(out, tool)
		}
	}
	return out
}

func remedyArgPaths(remedy guidance.RemedyEntry) []string {
	var out []string
	for path := range remedy.Args {
		out = append(out, path)
	}
	for path := range remedy.Pick {
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}

// acceptsArg reports whether a tool's schema carries an argument path. A tool
// with no bundled schema is described at runtime, so its arguments cannot be
// judged here.
func (c *Catalog) acceptsArg(tool, path string) bool {
	fields, ok := c.argFields[tool]
	return !ok || fields[path]
}
