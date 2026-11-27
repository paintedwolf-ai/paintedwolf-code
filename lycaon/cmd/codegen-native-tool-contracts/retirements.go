package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
)

// toolRetirement is one tools entry of the retirement catalog: a successor
// name list or a content contract.
type toolRetirement struct {
	Successors      []string
	ContentContract string
}

func (r *toolRetirement) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case bytes.HasPrefix(trimmed, []byte("[")):
		return json.Unmarshal(trimmed, &r.Successors)
	case bytes.HasPrefix(trimmed, []byte("{")):
		var ref struct {
			ContentContract string `json:"content_contract"`
		}
		if err := json.Unmarshal(trimmed, &ref); err != nil {
			return err
		}
		r.ContentContract = ref.ContentContract
		return nil
	}
	return fmt.Errorf("retirement successor must be a name list or a content contract object")
}

func loadToolRetirements(path string) (map[string]toolRetirement, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path from Taskfile
	if err != nil {
		return nil, fmt.Errorf("read retirements: %w", err)
	}
	var doc struct {
		Vocabularies struct {
			Tools map[string]toolRetirement `json:"tools"`
		} `json:"vocabularies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode retirements: %w", err)
	}
	for name, retirement := range doc.Vocabularies.Tools {
		if len(retirement.Successors) == 0 && retirement.ContentContract == "" {
			return nil, fmt.Errorf("retired tool %s records no successor", name)
		}
	}
	return doc.Vocabularies.Tools, nil
}

func renderRetirements(b *bytes.Buffer, retirements map[string]toolRetirement) {
	b.WriteString("// Retired tools and the successors schemas/agent-public-retirements.json records.\n")
	b.WriteString("var compiledRetirements = map[string]Retirement{\n")
	names := make([]string, 0, len(retirements))
	for name := range retirements {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		retirement := retirements[name]
		b.WriteString("\t")
		b.WriteString(strconv.Quote(name))
		b.WriteString(": {")
		if retirement.ContentContract != "" {
			b.WriteString("ContentContract: ")
			b.WriteString(strconv.Quote(retirement.ContentContract))
		} else {
			b.WriteString("Successors: []string{")
			for i, successor := range retirement.Successors {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strconv.Quote(successor))
			}
			b.WriteString("}")
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
}
