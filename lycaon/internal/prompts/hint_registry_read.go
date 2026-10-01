package prompts

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/hintregistry"
	"gopkg.in/yaml.v3"
)

// hintCodeRow projects policy data for tool-surface rendering.
// UnmarshalYAML flattens nested policy fields.
type hintCodeRow struct {
	Emit     string   `yaml:"emit"`
	Category string   `yaml:"category"`
	Tools    []string `yaml:"tools"`
	Instead  string   `yaml:"instead"`

	Copy      *hintCodeCopy       `yaml:"copy"`
	Selector  map[string][]string `yaml:"selector"`
	XEmit     string              `yaml:"x-paintedwolf-emit"`
	XCategory string              `yaml:"x-paintedwolf-category"`
}

type hintCodeCopy struct {
	Instead string `yaml:"instead"`
}

func (r *hintCodeRow) UnmarshalYAML(node *yaml.Node) error {
	type rowAlias hintCodeRow
	var raw rowAlias
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*r = hintCodeRow(raw)
	if r.Emit == "" {
		r.Emit = r.XEmit
	}
	if r.Category == "" {
		r.Category = r.XCategory
	}
	if r.Instead == "" && r.Copy != nil {
		r.Instead = r.Copy.Instead
	}
	if len(r.Tools) == 0 {
		r.Tools = append([]string(nil), r.Selector["tool"]...)
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
