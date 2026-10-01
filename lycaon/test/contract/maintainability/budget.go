// Package maintainability measures structural size across the checkout and
// holds every measured artifact to a reviewed cap.
package maintainability

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const budgetPath = "lycaon/test/contract/maintainability-budgets.yaml"
const refreshCommand = "UPDATE_MAINTAINABILITY_BUDGETS=1 ./task test:contract -- ./test/contract/maintainability -run '^TestMaintainabilityWithinBudget$'"

// measurements maps category → artifact ID → measured value (or cap).
type measurements map[string]map[string]int

type category struct{ unit, measures, refactor string }

var categories = map[string]category{
	"source_files":          {"lines", "Code-bearing physical lines of maintained production source", "Separate cohesive responsibilities; moving comments or squeezing statements does not improve structure."},
	"test_files":            {"lines", "Code-bearing physical lines of tests and test support", "Group scenarios by the behavior they prove and keep reusable fixtures explicit."},
	"source_directories":    {"files", "Immediate handwritten production source files", "Group cohesive features with clear dependencies; moving files alone does not reduce receiver coupling."},
	"test_directories":      {"files", "Immediate handwritten test and test-support source files", "Group tests by their contracts while preserving discovery and tier membership."},
	"go_struct_fields":      {"fields", "Named production struct fields; each name and embedded field counts", "Separate cohesive services with explicit dependencies; do not hide dependencies inside an unrestricted context."},
	"go_receiver_methods":   {"methods", "Distinct production receiver methods across package files", "Extract a cohesive responsibility with narrow dependencies; splitting files keeps this total unchanged."},
	"go_receiver_lines":     {"lines", "Code-bearing physical lines of production receiver methods across package files and build variants", "Reduce concentrated behavior by extracting a coherent service, preserving lifecycle and synchronization."},
	"ts_local_dependencies": {"modules", "Distinct resolved local imports and re-exports, including type-only and literal dynamic imports", "Clarify feature boundaries and dependencies; avoid a barrel that conceals the same coupling."},
}

func newMeasurements() measurements {
	out := measurements{}
	for name := range categories {
		out[name] = map[string]int{}
	}
	return out
}

// decodeBudgets rejects unknown categories and caps that are not plain
// nonnegative integers; the YAML decoder already rejects duplicate keys.
func decodeBudgets(raw []byte) (measurements, error) {
	var document map[string]map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode maintainability budgets: %w", err)
	}
	caps := measurements{}
	for name, artifacts := range document {
		if _, ok := categories[name]; !ok {
			return nil, fmt.Errorf("unknown maintainability category %q", name)
		}
		caps[name] = map[string]int{}
		for id, value := range artifacts {
			limit, ok := value.(int)
			if !ok || limit < 0 {
				return nil, fmt.Errorf("budget %s/%s must be a nonnegative integer", name, id)
			}
			caps[name][id] = limit
		}
	}
	return caps, nil
}

func encodeBudgets(measured measurements) ([]byte, error) {
	raw, err := yaml.Marshal(measured)
	if err != nil {
		return nil, fmt.Errorf("encode maintainability budgets: %w", err)
	}
	return append([]byte("# Measured caps. Refresh explicitly; review the diff and rerun without the flag.\n"), raw...), nil
}

type violation struct {
	category, id, kind string
	measured, cap      int
}

func compareBudgets(measured, caps measurements) []violation {
	var out []violation
	for name, artifacts := range measured {
		for id, value := range artifacts {
			if limit, ok := caps[name][id]; !ok {
				out = append(out, violation{name, id, "missing_cap", value, 0})
			} else if value > limit {
				out = append(out, violation{name, id, "over_cap", value, limit})
			}
		}
	}
	for name, artifacts := range caps {
		for id, limit := range artifacts {
			if _, ok := measured[name][id]; !ok {
				out = append(out, violation{name, id, "stale_cap", 0, limit})
			}
		}
	}
	slices.SortFunc(out, func(a, b violation) int {
		return cmp.Or(strings.Compare(a.category, b.category), strings.Compare(a.kind, b.kind), strings.Compare(a.id, b.id))
	})
	return out
}

// budgetReport names each violation, what it measures, and the remedy.
// sources lists the files behind artifacts whose ID is not itself a path.
func budgetReport(found []violation, sources map[string][]string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "maintainability budget: %d violation(s)\nCaps: %s\n", len(found), budgetPath)
	for _, v := range found {
		c := categories[v.category]
		fmt.Fprintf(&out, "\n%s: %s[%q]\nmeasures: %s\nmeasured: %d %s | cap: %d %s", v.kind, v.category, v.id, c.measures, v.measured, c.unit, v.cap, c.unit)
		if v.kind == "over_cap" {
			fmt.Fprintf(&out, " | excess: %d %s", v.measured-v.cap, c.unit)
		}
		out.WriteByte('\n')
		for _, path := range sources[v.id] {
			fmt.Fprintf(&out, "source: %s\n", path)
		}
		switch v.kind {
		case "stale_cap":
			out.WriteString("fix: remove this vanished artifact's cap.\n")
		case "missing_cap":
			out.WriteString("fix: add the measured cap for this new artifact.\n")
		default:
			fmt.Fprintf(&out, "refactor: %s\nfix: refactor, or deliberately bump this cap to the measured need.\n", c.refactor)
		}
	}
	fmt.Fprintf(&out, "\nRefresh all measured caps: %s\nReview every changed cap; rerun normally. Refresh never replaces independent architecture tests.\n", refreshCommand)
	return out.String()
}
