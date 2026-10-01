package conditions

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/blueprintfile"
)

// PlanFieldValue is one member of a plan field's closed vocabulary.
type PlanFieldValue struct {
	ID string `yaml:"id"`
	// OpensResearch reports whether this value leaves the research phase work to do.
	OpensResearch bool `yaml:"opens_research"`
}

// PlanField is a structured field a plan blueprint declares in frontmatter.
type PlanField struct {
	Key    string           `yaml:"key"`
	Values []PlanFieldValue `yaml:"values"`
}

// ValueIDs returns the declared vocabulary in catalogue order.
func (f PlanField) ValueIDs() []string {
	out := make([]string, 0, len(f.Values))
	for _, v := range f.Values {
		out = append(out, v.ID)
	}
	return out
}

// Prompt renders the field the way the agent must write it.
func (f PlanField) Prompt() string {
	return "`" + f.Key + ":` in frontmatter (" + strings.Join(f.ValueIDs(), " | ") + ")"
}

// Lookup resolves a written value by normalized equality: trimmed and lowercased,
// nothing else. A value outside the vocabulary is an unanswered field, not a near
// miss to interpret.
func (f PlanField) Lookup(written string) (PlanFieldValue, bool) {
	written = strings.ToLower(strings.TrimSpace(written))
	if written == "" {
		return PlanFieldValue{}, false
	}
	for _, v := range f.Values {
		if strings.ToLower(strings.TrimSpace(v.ID)) == written {
			return v, true
		}
	}
	return PlanFieldValue{}, false
}

// ResearchDepthFieldKey is the field whose declared value decides whether the
// research phase has anything to do.
const ResearchDepthFieldKey = "research_depth"

type planFieldsFile struct {
	Fields []PlanField `yaml:"fields"`
}

var loadPlanFields = sync.OnceValues(func() ([]PlanField, error) {
	data, err := config.Read(config.PlanBlueprintFields)
	if err != nil {
		return nil, fmt.Errorf("read plan blueprint fields: %w", err)
	}
	var file planFieldsFile
	if err := config.DecodeYAML(data, &file); err != nil {
		return nil, fmt.Errorf("parse plan blueprint fields: %w", err)
	}
	for _, f := range file.Fields {
		if strings.TrimSpace(f.Key) == "" {
			return nil, fmt.Errorf("plan blueprint field needs a key")
		}
		if len(f.Values) == 0 {
			return nil, fmt.Errorf("plan blueprint field %q declares no values", f.Key)
		}
		for _, v := range f.Values {
			if strings.TrimSpace(v.ID) == "" {
				return nil, fmt.Errorf("plan blueprint field %q has a value with no id", f.Key)
			}
		}
	}
	return file.Fields, nil
})

// PlanBlueprintFields returns the declared fields a plan stub must carry, and the
// load error when the catalogue is unreadable.
func PlanBlueprintFields() ([]PlanField, error) {
	fields, err := loadPlanFields()
	return append([]PlanField(nil), fields...), err
}

// planBlueprintField returns the declared field for key.
func planBlueprintField(key string) (PlanField, bool) {
	fields, err := loadPlanFields()
	if err != nil {
		return PlanField{}, false
	}
	for _, f := range fields {
		if f.Key == key {
			return f, true
		}
	}
	return PlanField{}, false
}

// PlanFieldDeclared returns the vocabulary member a blueprint's frontmatter names
// for key. It reads the YAML fence only, so no wording in the body reaches a gate.
func PlanFieldDeclared(text, key string) (PlanFieldValue, bool) {
	field, ok := planBlueprintField(key)
	if !ok {
		return PlanFieldValue{}, false
	}
	meta, _ := blueprintfile.SplitMarkdownFrontmatter(text)
	raw, present := meta[key]
	if !present || raw == nil {
		return PlanFieldValue{}, false
	}
	return field.Lookup(fmt.Sprint(raw))
}
