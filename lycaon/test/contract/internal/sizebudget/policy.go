// Package sizebudget holds measured artifacts to the size limits a category
// chose on purpose. Only growth past a chosen line fails; cleanup the policy
// could absorb is reported as a note with its fix.
package sizebudget

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limit is one category's warning line and hard limit.
type Limit struct {
	Warn  int `yaml:"warn" json:"warn"`
	Limit int `yaml:"limit" json:"limit"`
}

// MarshalYAML writes a limit on one line, so a policy reads as a table.
func (l Limit) MarshalYAML() (any, error) {
	var node yaml.Node
	if err := node.Encode(struct {
		Warn  int `yaml:"warn"`
		Limit int `yaml:"limit"`
	}{l.Warn, l.Limit}); err != nil {
		return nil, err
	}
	node.Style = yaml.FlowStyle
	return &node, nil
}

// Exception admits one artifact above its category limit for a stated reason.
type Exception struct {
	Cap    int    `yaml:"cap"`
	Reason string `yaml:"reason"`
}

// Policy is the reviewed limit state of one budget suite. Grandfathered caps
// record artifacts that predate their limit: refreshes only lower or drop
// them. Exceptions are hand-written and always carry a reason.
type Policy struct {
	Limits        map[string]Limit                `yaml:"limits"`
	Grandfathered map[string]map[string]int       `yaml:"grandfathered"`
	Exceptions    map[string]map[string]Exception `yaml:"exceptions"`
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
	for _, section := range []map[string]map[string]int{toCaps(p.Exceptions), p.Grandfathered} {
		for name := range section {
			if !known[name] {
				return fmt.Errorf("unknown category %q", name)
			}
		}
	}
	for name := range p.Limits {
		if !known[name] {
			return fmt.Errorf("unknown category %q", name)
		}
	}
	for name, entries := range p.Grandfathered {
		for id, limitCap := range entries {
			if limitCap <= p.Limits[name].Limit {
				return fmt.Errorf("grandfathered %s[%q] cap %d must exceed the limit %d", name, id, limitCap, p.Limits[name].Limit)
			}
		}
	}
	for name, entries := range p.Exceptions {
		for id, exception := range entries {
			if exception.Cap <= p.Limits[name].Limit {
				return fmt.Errorf("exception %s[%q] cap %d must exceed the limit %d", name, id, exception.Cap, p.Limits[name].Limit)
			}
			if strings.TrimSpace(exception.Reason) == "" {
				return fmt.Errorf("exception %s[%q] needs a reason", name, id)
			}
			if _, ok := p.Grandfathered[name][id]; ok {
				return fmt.Errorf("%s[%q] is both grandfathered and an exception", name, id)
			}
		}
	}
	return nil
}

// Cap returns the most an artifact may measure, and the entry that allows it.
func (p Policy) Cap(category, id string) (int, Entry) {
	if exception, ok := p.Exceptions[category][id]; ok {
		return exception.Cap, EntryException
	}
	if limitCap, ok := p.Grandfathered[category][id]; ok {
		return limitCap, EntryGrandfathered
	}
	return p.Limits[category].Limit, EntryNone
}

// Entry names what admits an artifact above its category limit.
type Entry string

const (
	EntryNone          Entry = ""
	EntryGrandfathered Entry = "grandfathered"
	EntryException     Entry = "exception"
)

// Kind classifies one finding.
type Kind string

const (
	// OverLimit: an artifact without an entry grew past its category limit.
	OverLimit Kind = "over_limit"
	// OverCap: an artifact with an entry grew past the cap it records.
	OverCap Kind = "over_cap"
	// OverWarn: an artifact passed its category's warning line.
	OverWarn Kind = "over_warn"
	// Slack: a grandfathered artifact shrank below its cap.
	Slack Kind = "slack"
	// Unneeded: an artifact with an entry now fits its category limit.
	Unneeded Kind = "unneeded"
	// Vanished: an entry names an artifact that no longer exists.
	Vanished Kind = "vanished"
	// GrandfatherRaised: a grandfathered cap was added or raised since the
	// change base; growth past a limit needs an exception and its reason.
	GrandfatherRaised Kind = "grandfather_raised"
)

// Fails reports whether a finding is growth past a chosen line.
func (k Kind) Fails() bool { return k == OverLimit || k == OverCap || k == GrandfatherRaised }

// Finding is one artifact's standing against the policy.
type Finding struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Measured int    `json:"measured"`
	// Bound is the line the finding is measured against: the cap, the limit,
	// or the warning line.
	Bound  int    `json:"bound"`
	Entry  Entry  `json:"entry,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Measurements maps category → artifact ID → measured size.
type Measurements map[string]map[string]int

// Evaluate classifies every measured artifact and every policy entry.
func Evaluate(p Policy, measured Measurements) []Finding {
	var out []Finding
	for name, artifacts := range measured {
		limit := p.Limits[name]
		for id, value := range artifacts {
			limitCap, entry := p.Cap(name, id)
			finding := Finding{Category: name, ID: id, Measured: value, Entry: entry, Reason: p.Exceptions[name][id].Reason}
			switch {
			case value > limitCap:
				finding.Kind, finding.Bound = OverLimit, limitCap
				if entry != EntryNone {
					finding.Kind = OverCap
				}
			case entry != EntryNone && value <= limit.Limit:
				finding.Kind, finding.Bound = Unneeded, limit.Limit
			case entry == EntryGrandfathered && value < limitCap:
				finding.Kind, finding.Bound = Slack, limitCap
			case entry == EntryNone && value > limit.Warn:
				finding.Kind, finding.Bound = OverWarn, limit.Warn
			default:
				continue
			}
			out = append(out, finding)
		}
	}
	for name, entries := range entryCaps(p) {
		for id, limitCap := range entries {
			if _, ok := measured[name][id]; !ok {
				_, entry := p.Cap(name, id)
				out = append(out, Finding{Category: name, ID: id, Kind: Vanished, Bound: limitCap, Entry: entry})
			}
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

// Tighten lowers grandfathered caps to their measurements and drops entries
// that no longer admit anything. It never adds an entry or raises a cap, and
// it leaves hand-written exceptions alone.
func Tighten(p Policy, measured Measurements) Policy {
	out := Policy{Limits: maps.Clone(p.Limits), Grandfathered: map[string]map[string]int{}, Exceptions: p.Exceptions}
	for name, entries := range p.Grandfathered {
		for id, limitCap := range entries {
			value, ok := measured[name][id]
			if !ok || value <= p.Limits[name].Limit {
				continue
			}
			if out.Grandfathered[name] == nil {
				out.Grandfathered[name] = map[string]int{}
			}
			out.Grandfathered[name][id] = min(limitCap, value)
		}
	}
	return out
}

func entryCaps(p Policy) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, section := range []map[string]map[string]int{p.Grandfathered, toCaps(p.Exceptions)} {
		for name, entries := range section {
			if out[name] == nil {
				out[name] = map[string]int{}
			}
			maps.Copy(out[name], entries)
		}
	}
	return out
}

func toCaps(exceptions map[string]map[string]Exception) map[string]map[string]int {
	out := map[string]map[string]int{}
	for name, entries := range exceptions {
		out[name] = map[string]int{}
		for id, exception := range entries {
			out[name][id] = exception.Cap
		}
	}
	return out
}

// RequireIntegers rejects a policy node whose sizes are not plain integers. A
// YAML decoder would otherwise truncate 700.5 to 700 or accept a quoted number.
func RequireIntegers(policy *yaml.Node) error {
	var walk func(node *yaml.Node, key string) error
	walk = func(node *yaml.Node, key string) error {
		switch node.Kind {
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
