// Package maintainability measures structural size across the checkout and
// holds every measured artifact to its category's limit.
package maintainability

import (
	"bytes"
	"fmt"

	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
	"gopkg.in/yaml.v3"
)

const policyPath = "lycaon/test/contract/maintainability-budgets.yaml"

var suite = sizebudget.Suite{
	Name:       "maintainability",
	PolicyPath: policyPath,
	Refresh:    "UPDATE_MAINTAINABILITY_BUDGETS=1 ./task budgets",
	Categories: map[string]sizebudget.Category{
		"source_files":          {Unit: "lines", Measures: "Code-bearing physical lines of maintained production source", Remedy: "Separate cohesive responsibilities; moving comments or squeezing statements does not improve structure."},
		"test_files":            {Unit: "lines", Measures: "Code-bearing physical lines of tests and test support", Remedy: "Group scenarios by the behavior they prove and keep reusable fixtures explicit."},
		"source_directories":    {Unit: "files", Measures: "Immediate handwritten production source files", Remedy: "Group cohesive features with clear dependencies; moving files alone does not reduce receiver coupling."},
		"test_directories":      {Unit: "files", Measures: "Immediate handwritten test and test-support source files", Remedy: "Group tests by their contracts while preserving discovery and tier membership."},
		"go_struct_fields":      {Unit: "fields", Measures: "Named production struct fields; each name and embedded field counts", Remedy: "Separate cohesive services with explicit dependencies; do not hide dependencies inside an unrestricted context."},
		"go_receiver_methods":   {Unit: "methods", Measures: "Distinct production receiver methods across package files", Remedy: "Extract a cohesive responsibility with narrow dependencies; splitting files keeps this total unchanged."},
		"go_receiver_lines":     {Unit: "lines", Measures: "Code-bearing physical lines of production receiver methods across package files and build variants", Remedy: "Reduce concentrated behavior by extracting a coherent service, preserving lifecycle and synchronization."},
		"ts_local_dependencies": {Unit: "modules", Measures: "Distinct resolved local imports and re-exports, including type-only and literal dynamic imports", Remedy: "Clarify feature boundaries and dependencies; avoid a barrel that conceals the same coupling."},
	},
}

// measurements maps category → artifact ID → measured value.
type measurements = sizebudget.Measurements

func newMeasurements() measurements {
	out := measurements{}
	for name := range suite.Categories {
		out[name] = map[string]int{}
	}
	return out
}

const policyHeader = `# Maintainability limits. An artifact past its category limit fails unless an
# entry admits it. Grandfathered caps predate their limits and only shrink:
# UPDATE_MAINTAINABILITY_BUDGETS=1 ./task budgets lowers and drops them.
# Exceptions are written by hand and say why the artifact must be that large.
`

// decodePolicy rejects unknown fields, unknown categories, duplicate keys,
// and entries a reviewer could not weigh.
func decodePolicy(raw []byte) (sizebudget.Policy, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("decode maintainability budgets: %w", err)
	}
	if len(document.Content) == 0 {
		return sizebudget.Policy{}, fmt.Errorf("maintainability budgets are empty")
	}
	if err := sizebudget.RequireIntegers(document.Content[0]); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("maintainability budgets: %w", err)
	}
	var policy sizebudget.Policy
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("decode maintainability budgets: %w", err)
	}
	if err := policy.Validate(suite.CategoryNames()); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("maintainability budgets: %w", err)
	}
	return policy, nil
}

func encodePolicy(policy sizebudget.Policy) ([]byte, error) {
	raw, err := yaml.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("encode maintainability budgets: %w", err)
	}
	return append([]byte(policyHeader), raw...), nil
}

// artifactSources names the files each artifact is made from, so the change
// report can tell which artifacts a change touched.
func artifactSources(inv *inventory) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for name, artifacts := range inv.measured {
		out[name] = map[string][]string{}
		for id := range artifacts {
			switch name {
			case "go_struct_fields", "go_receiver_methods", "go_receiver_lines":
				out[name][id] = inv.sources[id]
			default:
				out[name][id] = []string{id}
			}
		}
	}
	return out
}

// detail lists the files behind a Go type artifact, whose ID is not a path.
func detail(inv *inventory) func(sizebudget.Finding) []string {
	return func(f sizebudget.Finding) []string {
		var lines []string
		for _, file := range inv.sources[f.ID] {
			lines = append(lines, "source: "+file)
		}
		return lines
	}
}
