package detectionpack

import (
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"gopkg.in/yaml.v3"
)

// Rehearsal runs a pack's declared cases through the same adapters the approval
// gate and the egress broker use, so a rule that only matches a hand-built event
// fails here rather than reaching production as silence. Extension validation
// and the device-import preview run the same check.

// FixtureCorpus is one pack's fixtures.yaml.
type FixtureCorpus struct {
	Fixtures []FixtureRow `yaml:"fixtures"`
}

// FixtureRow is one rule's declared cases, keyed by the rule's file slug.
type FixtureRow struct {
	Rule     string        `yaml:"rule"`
	Positive []FixtureCase `yaml:"positive"`
	Negative []FixtureCase `yaml:"negative"`
}

// RowFor returns the row for a rule slug.
func (c FixtureCorpus) RowFor(slug string) (FixtureRow, bool) {
	for _, row := range c.Fixtures {
		if row.Rule == slug {
			return row, true
		}
	}
	return FixtureRow{}, false
}

// ParseFixtures decodes a fixtures.yaml document.
func ParseFixtures(data []byte) (FixtureCorpus, error) {
	var corpus FixtureCorpus
	if err := yaml.Unmarshal(data, &corpus); err != nil {
		return FixtureCorpus{}, fmt.Errorf("parse %s: %w", PackFixturesFile, err)
	}
	return corpus, nil
}

// Rehearsal outcome kinds. Stable strings: the CLI, the import preview, and
// Settings all branch on them.
const (
	// RehearsalPositiveMissed: a case the rule claims to catch, and did not.
	RehearsalPositiveMissed = "positive_missed"
	// RehearsalNegativeMatched: a case the pack declared safe, and some rule in
	// the pack fired on. Checked against every rule, not just the one that
	// declared it, so the rehearsal forms of an operation stay silent.
	RehearsalNegativeMatched = "negative_matched"
	// RehearsalRuleUncovered: a rule with no positive and negative case.
	RehearsalRuleUncovered = "rule_uncovered"
	// RehearsalUnknownRule: a fixture row naming a rule the pack does not have.
	RehearsalUnknownRule = "unknown_rule"
	// RehearsalRuleUnsupported: a rule outside the supported Sigma subset. It is
	// inert, so its fixtures were never exercised and prove nothing.
	RehearsalRuleUnsupported = "rule_unsupported"
)

// RehearsalFinding is one declared case that did not behave as declared.
type RehearsalFinding struct {
	PackID string
	Rule   string
	Kind   string
	// Case renders the action that was rehearsed, empty for whole-rule findings.
	Case string
	// Detail carries a whole-rule explanation, such as why a rule is inert.
	Detail string
}

// Message renders one finding for a person reading a validation report.
func (f RehearsalFinding) Message() string {
	switch f.Kind {
	case RehearsalPositiveMissed:
		return fmt.Sprintf("%s/%s: this rule did not match %q, which it declares it catches", f.PackID, f.Rule, f.Case)
	case RehearsalNegativeMatched:
		return fmt.Sprintf("%s/%s: %q is declared safe but a rule in this pack matched it", f.PackID, f.Rule, f.Case)
	case RehearsalRuleUncovered:
		return fmt.Sprintf("%s/%s: needs at least one positive and one negative case in %s", f.PackID, f.Rule, PackFixturesFile)
	case RehearsalUnknownRule:
		return fmt.Sprintf("%s: %s names rule %q, which this pack does not have", f.PackID, PackFixturesFile, f.Rule)
	case RehearsalRuleUnsupported:
		return fmt.Sprintf("%s/%s: this rule does not run, so nothing it declares was checked — %s",
			f.PackID, f.Rule, f.Detail)
	default:
		return fmt.Sprintf("%s/%s: %s %s", f.PackID, f.Rule, f.Kind, f.Case)
	}
}

// Rehearse runs one pack's corpus through the production adapters and returns
// everything that did not hold. An unsupported rule is reported rather than
// skipped: it is inert, and its silence looks like a rule that matched nothing.
func Rehearse(p Pack, corpus FixtureCorpus, semantics *ActionSemanticsCatalog) []RehearsalFinding {
	var findings []RehearsalFinding
	bySlug := map[string]Rule{}
	for _, r := range p.Rules {
		bySlug[r.Slug] = r
	}
	for _, row := range corpus.Fixtures {
		rule, known := bySlug[row.Rule]
		if !known {
			findings = append(findings, RehearsalFinding{PackID: p.ID, Rule: row.Rule, Kind: RehearsalUnknownRule})
			continue
		}
		if !rule.Supported {
			// Reported once per rule in the sweep below, not once per case.
			continue
		}
		for _, positive := range row.Positive {
			if !ruleMatchesCase(p, rule, positive, semantics) {
				findings = append(findings, RehearsalFinding{
					PackID: p.ID, Rule: row.Rule, Kind: RehearsalPositiveMissed, Case: positive.Render(),
				})
			}
		}
		for _, negative := range row.Negative {
			if anyRuleMatchesCase(p, negative, semantics) {
				findings = append(findings, RehearsalFinding{
					PackID: p.ID, Rule: row.Rule, Kind: RehearsalNegativeMatched, Case: negative.Render(),
				})
			}
		}
	}
	for _, r := range p.Rules {
		if !r.Supported {
			findings = append(findings, RehearsalFinding{
				PackID: p.ID, Rule: r.Slug, Kind: RehearsalRuleUnsupported, Detail: r.UnsupportedReason,
			})
			continue
		}
		row, ok := corpus.RowFor(r.Slug)
		if !ok || len(row.Positive) == 0 || len(row.Negative) == 0 {
			findings = append(findings, RehearsalFinding{PackID: p.ID, Rule: r.Slug, Kind: RehearsalRuleUncovered})
		}
	}
	return findings
}

// Render describes one case the way it was declared.
func (c FixtureCase) Render() string {
	if strings.TrimSpace(c.Command) != "" {
		return c.Command
	}
	if len(c.Args) > 0 {
		tool := c.Tool
		if tool == "" {
			tool = "command"
		}
		return fmt.Sprintf("%s %v", tool, c.Args)
	}
	return strings.Join(c.TargetFiles, " ")
}

// rehearsalPosture is the strictest posture, so a case is judged by whether the
// rule matched at all rather than by which band the reader happens to run.
const rehearsalPosture = gate.PostureStrict

// ruleMatchesCase enters one case through the adapter its log source names.
func ruleMatchesCase(p Pack, rule Rule, fixture FixtureCase, semantics *ActionSemanticsCatalog) bool {
	solo := &Catalog{Packs: []Pack{{ID: p.ID, Enabled: true, Rules: []Rule{rule}}}}
	if rule.Source == SourceEgressObserved {
		hit, ok := NewEgressSource(NewMatcher(solo)).Match(EgressObservation{
			DestinationHostname: fixture.Command,
			DestinationPort:     443,
			Transport:           "http_connect",
			SessionID:           "rehearsal",
			ActionID:            "rehearsal-action",
		}, "", "")
		return ok && hit.RuleID == rule.ID
	}
	tool := fixture.Tool
	if tool == "" {
		tool = "command"
	}
	args := maps.Clone(fixture.Args)
	if args == nil {
		args = map[string]any{}
	}
	if strings.TrimSpace(fixture.Command) != "" {
		if _, exists := args["command"]; !exists {
			args["command"] = fixture.Command
		}
	}
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tool,
Args: args,
Files: fixture.TargetFiles,
ActionID: "rehearsal-action",
},
Resources: hitl.ActionResources{
ApprovalCategory: fixture.ApprovalCategory,
ApprovalSubject: fixture.ApprovalSubject,
},
Scope: hitl.ActionScope{
ProjectDir: "/tmp/proj",
SessionID: "rehearsal",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{
			FSJailed: true,
			Egress:   "proxy",
			Roots:    []string{"/tmp/proj"},
		},
},
}
	source := NewGateSource(NewMatcher(solo), semantics)
	// Each rule rehearses through the path it takes. A mint rule carries an
	// inert level and never reaches the ask path.
	if EffectFromTags(rule.Tags).MintsCredential {
		hit, ok := source.MintedCredentialRule(action)
		return ok && hit.RuleID == rule.ID
	}
	hit, ok := source.MatchAction(action, rehearsalPosture)
	return ok && hit.RuleID == rule.ID
}

// anyRuleMatchesCase is the negative check: a case declared safe must be silent
// against the whole pack, not only against the rule that declared it.
func anyRuleMatchesCase(p Pack, fixture FixtureCase, semantics *ActionSemanticsCatalog) bool {
	for _, r := range p.Rules {
		if !r.Supported {
			continue
		}
		if ruleMatchesCase(p, r, fixture, semantics) {
			return true
		}
	}
	return false
}
