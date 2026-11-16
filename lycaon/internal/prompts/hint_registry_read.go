package prompts

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/oarcore"

	"github.com/lycaon/lycaon/internal/hintregistry"
	"gopkg.in/yaml.v3"
)

// hintCodeRow projects policy data for tool-surface rendering.
type hintCodeRow struct {
	SpecVersion string   `yaml:"spec_version"`
	Emit        string   `yaml:"x-paintedwolf-emit"`
	Category    string   `yaml:"x-paintedwolf-category"`
	Tools       []string `yaml:"tools"`
	Instead     string   `yaml:"instead"`
	When        string   `yaml:"when"`

	Selector hintCodeSelector `yaml:"selector"`
}

type hintCodeSelector struct {
	Tools []string `yaml:"tools"`
}

func (r *hintCodeRow) UnmarshalYAML(node *yaml.Node) error {
	type rowAlias hintCodeRow
	var raw rowAlias
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*r = hintCodeRow(raw)
	// A copy-only unit is not an OAR document and keeps plain metadata names.
	if r.SpecVersion == "" {
		var flat struct {
			Emit     string `yaml:"emit"`
			Category string `yaml:"category"`
		}
		if err := node.Decode(&flat); err != nil {
			return err
		}
		r.Emit, r.Category = flat.Emit, flat.Category
	}
	if len(r.Tools) == 0 {
		r.Tools = append([]string(nil), r.Selector.Tools...)
	}
	// An operation selector applies where that operation's structured tool is offered.
	if len(r.Tools) == 0 {
		r.Tools = oarcore.ConjunctValues(r.When, "paintedwolf.operation")
	}
	return nil
}

// LoadHintCodeRows loads effective policy rows.
func LoadHintCodeRows() (map[string]hintCodeRow, error) {
	entries, err := hintregistry.ListEffective()
	if err != nil {
		return nil, err
	}
	return LoadHintCodeRowsFromEntries(entries)
}

// LoadHintCodeRowsFromEntries projects policy entries into hint code rows.
func LoadHintCodeRowsFromEntries(entries []hintregistry.Entry) (map[string]hintCodeRow, error) {
	out := make(map[string]hintCodeRow, len(entries))
	for _, ent := range entries {
		var row hintCodeRow
		if err := yaml.Unmarshal(ent.Body, &row); err != nil {
			return nil, fmt.Errorf("%s: %w", ent.Path, err)
		}
		out[ent.Code] = row
	}
	return out, nil
}
