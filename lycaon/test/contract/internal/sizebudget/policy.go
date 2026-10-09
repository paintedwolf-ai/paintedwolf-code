// Package sizebudget evaluates absolute limits, explicit caps, and measured growth.
package sizebudget

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limit is one category's warning line and hard limit.
type Limit struct {
	Warn  int `yaml:"warn" json:"warn"`
	Limit int `yaml:"limit" json:"limit"`
}

// Exception admits one artifact above its category limit, up to its cap, for
// a reason a reviewer can weigh.
type Exception struct {
	Cap    int    `yaml:"cap"`
	Reason string `yaml:"reason"`
}

// Policy is the reviewed limit state of one budget suite.
type Policy struct {
	Limits     map[string]Limit                `yaml:"limits"`
	Exceptions map[string]map[string]Exception `yaml:"exceptions"`
}

// Validate checks the policy against the categories its suite measures.
func (p Policy) Validate(categories []string) error {
	known := map[string]bool{}
	for _, name := range categories {
		known[name] = true
		limit, ok := p.Limits[name]
		if !ok {
			return fmt.Errorf("category %s has no limit", name)
		}
		if limit.Warn <= 0 || limit.Warn >= limit.Limit {
			return fmt.Errorf("category %s needs 0 < warn (%d) < limit (%d)", name, limit.Warn, limit.Limit)
		}
	}
	for name := range p.Limits {
		if !known[name] {
			return fmt.Errorf("unknown category %q", name)
		}
	}
	for name, entries := range p.Exceptions {
		if !known[name] {
			return fmt.Errorf("unknown category %q", name)
		}
		for id, exception := range entries {
			if exception.Cap <= p.Limits[name].Limit {
				return fmt.Errorf("exception %s[%q] cap %d must exceed the limit %d", name, id, exception.Cap, p.Limits[name].Limit)
			}
			if strings.TrimSpace(exception.Reason) == "" {
				return fmt.Errorf("exception %s[%q] needs a reason", name, id)
			}
		}
	}
	return nil
}

// Cap returns the most an artifact may measure, and whether an exception sets it.
func (p Policy) Cap(category, id string) (int, bool) {
	if exception, ok := p.Exceptions[category][id]; ok {
		return exception.Cap, true
	}
	return p.Limits[category].Limit, false
}

// RequireIntegers rejects a policy node whose sizes are not plain integers. A
// YAML decoder would otherwise truncate 700.5 to 700 or accept a quoted number.
// Policies are mappings of sizes, so a list or an alias, which could carry a
// size past this check, is rejected too.
func RequireIntegers(policy *yaml.Node) error {
	var walk func(node *yaml.Node, key string) error
	walk = func(node *yaml.Node, key string) error {
		switch node.Kind {
		case yaml.DocumentNode:
			for _, child := range node.Content {
				if err := walk(child, key); err != nil {
					return err
				}
			}
		case yaml.SequenceNode, yaml.AliasNode:
			return fmt.Errorf("line %d: %s must be a mapping or a plain integer", node.Line, key)
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				if err := walk(node.Content[i+1], node.Content[i].Value); err != nil {
					return err
				}
			}
		case yaml.ScalarNode:
			if key != "reason" && node.Tag != "!!int" {
				return fmt.Errorf("line %d: %s must be a plain integer, not %q", node.Line, key, node.Value)
			}
		}
		return nil
	}
	return walk(policy, "")
}

// Kind classifies one finding.
type Kind string

const (
	// OverLimit: an artifact the change touched is past its category limit
	// and no exception admits it.
	OverLimit  Kind = "over_limit"
	LegacyDebt Kind = "legacy_debt"
	// OverCap: an artifact is past the ceiling its exception records.
	OverCap Kind = "over_cap"
	// OverWarn: an artifact the change touched is past its warning line.
	OverWarn Kind = "over_warn"
	// Excepted: the change touched an artifact an exception admits.
	Excepted Kind = "excepted"
	// Unneeded: an artifact with an exception now fits its category limit.
	Unneeded Kind = "unneeded"
	// Vanished: an exception names an artifact that no longer exists.
	Vanished Kind = "vanished"
	// ExceptionAdded: this change added an exception or raised its cap.
	ExceptionAdded Kind = "exception_added"
)

// Fails reports whether a finding stops the change.
func (k Kind) Fails() bool { return k == OverLimit || k == OverCap }

// Finding is one artifact's standing against the policy.
type Finding struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Measured int    `json:"measured"`
	// Bound is the line the finding is measured against: an exception cap,
	// the category limit, or the warning line.
	Bound  int    `json:"bound"`
	Reason string `json:"reason,omitempty"`
	// Previous is a raised exception's former cap.
	Previous *int `json:"previous,omitempty"`
}

// Measurements maps category → artifact ID → measured size.
type Measurements map[string]map[string]int

// Touched reports whether a change touched one artifact.
type Touched func(category, id string) bool

// Evaluate holds every artifact to an absolute standard. An artifact the
// change touched must be within its category limit, or within the cap of the
// exception that says why it must be larger. No exception may be outgrown,
// touched or not. Untouched artifacts past their limit are counted, not
// failed: the standard applies when someone next works in them.
func Evaluate(p Policy, measured Measurements, touched Touched) []Finding {
	var out []Finding
	for name, artifacts := range measured {
		limit := p.Limits[name]
		for id, value := range artifacts {
			finding := Finding{Category: name, ID: id, Measured: value}
			exception, excepted := p.Exceptions[name][id]
			switch {
			case excepted && value > exception.Cap:
				finding.Kind, finding.Bound, finding.Reason = OverCap, exception.Cap, exception.Reason
			case excepted && value <= limit.Limit:
				finding.Kind, finding.Bound, finding.Reason = Unneeded, limit.Limit, exception.Reason
			case !touched(name, id):
				continue
			case excepted:
				finding.Kind, finding.Bound, finding.Reason = Excepted, exception.Cap, exception.Reason
			case value > limit.Limit:
				finding.Kind, finding.Bound = OverLimit, limit.Limit
			case value > limit.Warn:
				finding.Kind, finding.Bound = OverWarn, limit.Warn
			default:
				continue
			}
			out = append(out, finding)
		}
	}
	for name, entries := range p.Exceptions {
		for id, exception := range entries {
			if _, ok := measured[name][id]; !ok {
				out = append(out, Finding{Category: name, ID: id, Kind: Vanished, Bound: exception.Cap, Reason: exception.Reason})
			}
		}
	}
	sortFindings(out)
	return out
}

// Untouched counts, per category, the artifacts past their limit that no
// exception admits and the change did not touch.
func Untouched(p Policy, measured Measurements, touched Touched) map[string]int {
	out := map[string]int{}
	for name, artifacts := range measured {
		for id, value := range artifacts {
			if _, excepted := p.Exceptions[name][id]; !excepted && value > p.Limits[name].Limit && !touched(name, id) {
				out[name]++
			}
		}
	}
	return out
}

// ExceptionChanges reports exceptions this change added or raised, so a
// reviewer weighs each one alongside the growth it admits.
func ExceptionChanges(base, head Policy) []Finding {
	var out []Finding
	for name, entries := range head.Exceptions {
		for id, exception := range entries {
			previous, ok := base.Exceptions[name][id]
			if ok && exception.Cap <= previous.Cap {
				continue
			}
			finding := Finding{Category: name, ID: id, Kind: ExceptionAdded, Measured: exception.Cap, Reason: exception.Reason}
			if ok {
				finding.Previous = &previous.Cap
			}
			out = append(out, finding)
		}
	}
	sortFindings(out)
	return out
}

func sortFindings(findings []Finding) {
	slices.SortFunc(findings, func(a, b Finding) int {
		return cmp.Or(strings.Compare(a.Category, b.Category), strings.Compare(string(a.Kind), string(b.Kind)), strings.Compare(a.ID, b.ID))
	})
}

// Failures returns the findings that fail the suite.
func Failures(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Kind.Fails() {
			out = append(out, f)
		}
	}
	return out
}
