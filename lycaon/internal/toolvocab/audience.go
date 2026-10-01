package toolvocab

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
)

// audienceClass says which profiles can receive a rule fired at an anchor.
// Anchor ids are the host's local names: the loader maps core anchors through
// the capability document first, so agent.finalize arrives as worker.finalize.
//
// The table is closed. An unclassified anchor fails the load rather than
// exempting its rules from remedy reachability.
type audienceClass int

const (
	// audienceAnyCaller is the tool-invocation path: any profile holding the
	// selected tool can trigger the rule.
	audienceAnyCaller audienceClass = iota
	audienceCoordinator
	audienceWorker
)

var anchorAudience = map[string]audienceClass{
	oar.AnchorToolPreInvoke:            audienceAnyCaller,
	oar.AnchorToolHandler:              audienceAnyCaller,
	oar.AnchorToolRejected:             audienceAnyCaller,
	oar.AnchorToolPost:                 audienceAnyCaller,
	oar.AnchorCredentialAssignment:     audienceAnyCaller,
	oar.AnchorSessionPreInvoke:         audienceAnyCaller,
	oar.AnchorContentInput:             audienceAnyCaller,
	oar.AnchorContentOutput:            audienceAnyCaller,
	oar.AnchorContentToolResult:        audienceAnyCaller,
	oar.AnchorCoordinatorPreInvoke:     audienceCoordinator,
	oar.AnchorCoordinatorPostTurn:      audienceAnyCaller,
	oar.AnchorCoordinatorCloseoutCheck: audienceCoordinator,
	oar.AnchorWorkerFinalize:           audienceWorker,
	oar.AnchorWorkerReportCheck:        audienceWorker,
}

// Surfaces names which profile plays which role, resolved from the agent registry.
type Surfaces struct {
	Coordinator string
	Workers     []string
}

// Audience returns the profiles that can receive rule, sorted. An explicit
// x-paintedwolf-audience wins; otherwise the anchor picks the role and selector.tool
// narrows within it.
func Audience(c *Catalog, s Surfaces, rule *oar.Rule) ([]string, error) {
	if rule == nil {
		return nil, fmt.Errorf("nil rule")
	}
	if declared := rule.Audience; len(declared) > 0 {
		for _, id := range declared {
			if !c.HasProfile(id) {
				return nil, fmt.Errorf("rule %q: x-paintedwolf-audience names unknown tool profile %q", rule.ID, id)
			}
		}
		out := append([]string(nil), declared...)
		sort.Strings(out)
		return out, nil
	}
	class, ok := anchorAudience[strings.TrimSpace(rule.Anchor)]
	if !ok {
		return nil, fmt.Errorf(
			"rule %q: anchor %q has no audience classification — add a row to anchorAudience rather than leaving its rules unchecked",
			rule.ID, rule.Anchor)
	}
	var candidates []string
	switch class {
	case audienceCoordinator:
		candidates = []string{s.Coordinator}
	case audienceWorker:
		candidates = append([]string(nil), s.Workers...)
	case audienceAnyCaller:
		candidates = c.ProfileIDs()
	}
	selected := rule.Selector["tool"]
	if len(selected) == 0 {
		sort.Strings(candidates)
		return candidates, nil
	}
	var out []string
	for _, id := range candidates {
		for _, tool := range selected {
			if c.ProfileHolds(id, tool) {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// namedTool matches backticked names — how copy tells a reader to call
// something, as opposed to a word that collides with a tool name.
var namedTool = regexp.MustCompile("`([a-z][a-z0-9_]*)(?:`|[ \t]*\\()")

// guardedRegion strips {% if %} blocks: a tool named inside one is already
// gated on a declared fact, and that fact is the audience filter.
var guardedRegion = regexp.MustCompile(`(?s)\{%\s*if\b.*?\{%\s*endif\s*%\}`)

// NamedTools returns the tools that copy unconditionally tells its reader to
// call, in source order and deduplicated.
func NamedTools(c *Catalog, copyText string) []string {
	unguarded := guardedRegion.ReplaceAllString(copyText, " ")
	seen := map[string]bool{}
	var out []string
	for _, m := range namedTool.FindAllStringSubmatch(unguarded, -1) {
		name := m[1]
		if seen[name] || !c.KnownName(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// CheckRules resolves the tool names a rule set authors. Selector clauses and
// scenario fixtures must name real tools; Fix and Instead must name tools the
// rule's audience holds.
//
// An unreachable remedy is an error within one authority and a note across two.
func CheckRules(c *Catalog, s Surfaces, rules *oar.RuleSet) (notes []string, err error) {
	if rules == nil {
		return nil, fmt.Errorf("rule set required")
	}
	var problems []string
	for _, rule := range rules.All() {
		for _, tool := range rule.Selector["tool"] {
			if !c.KnownPattern(tool) {
				problems = append(problems, fmt.Sprintf(
					"rule %q: selector.tool names %q, which resolves to no registered tool — the clause can never match",
					rule.ID, tool))
			}
		}
		for _, sc := range rule.Scenarios {
			tool, _ := sc.Vars["tool"].(string)
			if tool = strings.TrimSpace(tool); tool != "" && !c.KnownName(tool) {
				problems = append(problems, fmt.Sprintf(
					"rule %q scenario %q: vars.tool is %q, which is not a registered tool — the fixture would certify a name the host cannot produce",
					rule.ID, sc.ID, tool))
			}
		}
		audience, err := Audience(c, s, rule)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		own, other := unreachableRemedies(c, rule, audience)
		problems = append(problems, own...)
		notes = append(notes, other...)
	}
	sort.Strings(notes)
	return notes, joinProblems(problems)
}

// unreachableRemedies splits by authority: own holds profiles the rule's author
// wrote, other holds everybody else's.
func unreachableRemedies(c *Catalog, rule *oar.Rule, audience []string) (own, other []string) {
	if len(audience) == 0 {
		return nil, nil
	}
	selected := map[string]bool{}
	for _, tool := range rule.Selector["tool"] {
		selected[tool] = true
	}
	for _, field := range []struct {
		name string
		text string
	}{{"fix", rule.Fix}, {"instead", rule.Copy.Instead}} {
		for _, tool := range NamedTools(c, field.text) {
			// The rejected call is the subject of the rule, not a remedy.
			if selected[tool] {
				continue
			}
			lacking := c.lacking(audience, tool)
			if len(lacking) == 0 {
				continue
			}
			var mine, theirs []string
			for _, id := range lacking {
				if c.provenance.sameAuthority(rule.ID, id) {
					mine = append(mine, id)
					continue
				}
				theirs = append(theirs, id)
			}
			if len(mine) > 0 {
				own = append(own, fmt.Sprintf(
					"rule %q: copy.%s tells the reader to call %q, but profile(s) %s can receive this rule and do not hold it — "+
						"narrow the rule with x-paintedwolf-audience, guard the sentence with a declared fact, or name a remedy every reader can reach",
					rule.ID, field.name, tool, strings.Join(mine, ", ")))
			}
			if len(theirs) > 0 {
				other = append(other, fmt.Sprintf(
					"rule %q names %q as a remedy, which profile(s) %s do not hold — an agent on one of those profiles "+
						"will be told to call a tool it cannot reach",
					rule.ID, tool, strings.Join(theirs, ", ")))
			}
		}
	}
	return own, other
}
