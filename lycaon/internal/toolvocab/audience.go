package toolvocab

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolschema"
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
	oar.AnchorCoordinatorMidTurnNote:   audienceCoordinator,
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
	selected, operations := rule.Selector["tool"], rule.Selector["paintedwolf.operation"]
	if len(selected) == 0 && len(operations) == 0 {
		sort.Strings(candidates)
		return candidates, nil
	}
	var out []string
	for _, id := range candidates {
		if receives(c, id, selected, operations) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// receives reports whether a profile can reach a rule's selected occurrence.
// An operation is reached through its structured tool or beneath command.
func receives(c *Catalog, id string, tools, operations []string) bool {
	if len(tools) > 0 && !holdsAny(c, id, tools) {
		return false
	}
	return len(operations) == 0 || holdsAny(c, id, operations) || c.ProfileHolds(id, "command")
}

func holdsAny(c *Catalog, id string, tools []string) bool {
	for _, tool := range tools {
		if c.ProfileHolds(id, tool) {
			return true
		}
	}
	return false
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
			if c.operations[strings.TrimSpace(tool)] {
				notes = append(notes, fmt.Sprintf(
					"rule %q: selector.tool names %q, which no longer exists as a tool, so the clause never matches; use the paintedwolf.operation selector",
					rule.ID, tool))
				continue
			}
			if !c.KnownPattern(tool) {
				problems = append(problems, fmt.Sprintf(
					"rule %q: selector.tool names %q, which resolves to no registered tool — the clause can never match",
					rule.ID, tool))
			}
		}
		for _, operation := range rule.Selector["paintedwolf.operation"] {
			if !c.KnownPattern(operation) && !c.knownOperationPattern(operation) {
				problems = append(problems, fmt.Sprintf(
					"rule %q: selector paintedwolf.operation names %q, which resolves to no tool or operation — the clause can never match",
					rule.ID, operation))
			}
		}
		for _, sc := range rule.Scenarios {
			tool, _ := sc.Vars["tool"].(string)
			if tool = strings.TrimSpace(tool); tool != "" && !c.KnownName(tool) && !c.operations[tool] {
				problems = append(problems, fmt.Sprintf(
					"rule %q scenario %q: vars.tool is %q, which is not a registered tool or operation — the fixture would certify a name the host cannot produce",
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
		unchecked := uncheckedCalls(c, rule)
		if c.provenance.rulePack(rule.ID) == "" {
			problems = append(problems, unchecked...)
		} else {
			notes = append(notes, unchecked...)
		}
	}
	sort.Strings(notes)
	return notes, joinProblems(problems)
}

// ToolsIn lists the current and retired tools rendered text names as calls, sorted.
func ToolsIn(c *Catalog, text string) []string {
	seen := map[string]bool{}
	for _, m := range namedTool.FindAllStringSubmatch(text, -1) {
		if c.registered[m[1]] || c.operations[m[1]] {
			seen[m[1]] = true
		}
	}
	return sortedKeys(seen)
}

// copiedName matches a name copy can state outside backticks: one with an
// underscore cannot be an ordinary word.
var copiedName = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// uncheckedCalls lists what a rule tells its reader about tools without the host
// having checked it: a tool in copy that is neither the rule's subject nor a
// declared mention, and a next step that could not be submitted as written.
func uncheckedCalls(c *Catalog, rule *oar.Rule) []string {
	declared := map[string]bool{}
	for _, tool := range append(append([]string(nil), rule.Selector["tool"]...), rule.Selector["paintedwolf.operation"]...) {
		declared[tool] = true
	}
	var steps []oar.NextStep
	if rule.NextSteps != nil {
		for _, tool := range rule.NextSteps.Mentions {
			declared[tool] = true
		}
		steps = rule.NextSteps.Steps
	}
	var out []string
	members := []struct{ name, text string }{{"what", rule.Copy.What}, {"cause", rule.Copy.Cause}, {"why", rule.Copy.Why}, {"fix", rule.Copy.Fix}, {"instead", rule.Copy.Instead}}
	for _, member := range members {
		// Fact bindings are data names, not claims about tools.
		text := copyBinding.ReplaceAllString(member.text, " ")
		seen := map[string]bool{}
		names := copiedName.FindAllString(text, -1)
		for _, m := range namedTool.FindAllStringSubmatch(text, -1) {
			names = append(names, m[1])
		}
		for _, name := range names {
			if seen[name] || declared[name] || !(c.registered[name] || c.operations[name]) {
				continue
			}
			seen[name] = true
			out = append(out, fmt.Sprintf(
				"rule %q: copy.%s names %q; offer it as a next step the host checks, or declare it a mention in host/next-steps",
				rule.ID, member.name, name))
		}
	}
	for _, step := range steps {
		out = append(out, c.uncheckedStep(rule.ID, step)...)
	}
	return out
}

var copyBinding = regexp.MustCompile(`\{[{%].*?[%}]\}`)

func (c *Catalog) uncheckedStep(ruleID string, step oar.NextStep) []string {
	var out []string
	if tool := strings.TrimSpace(step.Tool); tool != "" {
		switch meta, described := c.schemas.ToolMeta(tool); {
		case !c.KnownName(tool):
			out = append(out, fmt.Sprintf("rule %q: next step calls %q, which is not a current tool", ruleID, tool))
		case described:
			out = append(out, stepArgProblems(ruleID, step, meta.ArgsSchema)...)
		}
	}
	for _, alternative := range step.Or {
		out = append(out, c.uncheckedStep(ruleID, alternative)...)
	}
	return out
}

func stepArgProblems(ruleID string, step oar.NextStep, schema map[string]any) []string {
	known := map[string]bool{}
	for _, path := range toolschema.ArgFieldPaths(schema) {
		known[path] = true
	}
	var out []string
	given := map[string]bool{}
	note := func(path string) {
		given[strings.SplitN(path, ".", 2)[0]] = true
		if !known[path] {
			out = append(out, fmt.Sprintf("rule %q: next step calling %q sets %q, which its schema does not declare", ruleID, step.Tool, path))
		}
	}
	for path, value := range step.Args {
		if nested, ok := value.(map[string]any); ok {
			for key := range nested {
				note(path + "." + key)
			}
			continue
		}
		note(path)
	}
	for path := range step.Pick {
		note(path)
	}
	if step.Complete {
		required, _ := toolschema.ArgFieldSummary(schema)
		for _, name := range required {
			if !given[strings.TrimSuffix(name, "?")] {
				out = append(out, fmt.Sprintf("rule %q: next step marks its %q call complete without required %q", ruleID, step.Tool, name))
			}
		}
	}
	return out
}

// unreachableRemedies splits by authority: own holds profiles the rule's author
// wrote, other holds everybody else's.
func unreachableRemedies(c *Catalog, rule *oar.Rule, audience []string) (own, other []string) {
	if len(audience) == 0 {
		return nil, nil
	}
	selected := map[string]bool{}
	for _, tool := range append(append([]string(nil), rule.Selector["tool"]...), rule.Selector["paintedwolf.operation"]...) {
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
