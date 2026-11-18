package main

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type componentDefinitions struct {
	Parameters map[string]yaml.Node `yaml:"parameters"`
	Schemas    map[string]yaml.Node `yaml:"schemas"`
}

type parameterSpec struct {
	Name     string    `yaml:"name"`
	In       string    `yaml:"in"`
	Required bool      `yaml:"required"`
	Style    string    `yaml:"style"`
	Explode  *bool     `yaml:"explode"`
	Schema   yaml.Node `yaml:"schema"`
	Content  yaml.Node `yaml:"content"`
}

type queryParameter struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Required   bool     `json:"required,omitempty"`
	Repeatable bool     `json:"repeatable,omitempty"`
	Minimum    *float64 `yaml:"minimum" json:"minimum,omitempty"`
	Maximum    *float64 `yaml:"maximum" json:"maximum,omitempty"`
	Enum       []any    `json:"enum,omitempty"`
	MinLength  *uint    `yaml:"minLength" json:"minLength,omitempty"`
	MaxLength  *uint    `yaml:"maxLength" json:"maxLength,omitempty"`
	MinItems   *uint    `json:"minItems,omitempty"`
	MaxItems   *uint    `json:"maxItems,omitempty"`
}

func (defs componentDefinitions) queryParameters(pathParams, operationParams []yaml.Node) ([]queryParameter, error) {
	merged := make(map[string]parameterSpec)
	for _, level := range [][]yaml.Node{pathParams, operationParams} {
		seen := make(map[string]bool)
		for _, item := range level {
			parameter, err := defs.resolveParameter(item)
			if err != nil {
				return nil, err
			}
			if parameter.In != "query" {
				continue
			}
			if seen[parameter.Name] {
				return nil, fmt.Errorf("duplicate query parameter %q", parameter.Name)
			}
			seen[parameter.Name] = true
			merged[parameter.Name] = parameter
		}
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	slices.Sort(names)
	var query []queryParameter
	for _, name := range names {
		parameter, err := defs.queryParameter(merged[name])
		if err != nil {
			return nil, fmt.Errorf("query parameter %q: %w", name, err)
		}
		query = append(query, parameter)
	}
	return query, nil
}

func (defs componentDefinitions) resolveParameter(item yaml.Node) (parameterSpec, error) {
	var parameter parameterSpec
	node, err := resolveQueryReference(item, "parameters", defs.Parameters)
	if err != nil {
		return parameter, err
	}
	if err := node.Decode(&parameter); err != nil {
		return parameter, err
	}
	if parameter.Name == "" || parameter.In == "" {
		return parameter, fmt.Errorf("parameter requires name and in")
	}
	return parameter, nil
}

func resolveQueryReference(node yaml.Node, kind string, definitions map[string]yaml.Node) (yaml.Node, error) {
	visited := make(map[string]bool)
	for {
		if node.Kind != yaml.MappingNode {
			return yaml.Node{}, fmt.Errorf("expected %s mapping", kind)
		}
		ref := mappingValue(&node, "$ref")
		if ref == nil {
			return node, nil
		}
		for i := 0; i < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "$ref", "summary", "description":
			default:
				return yaml.Node{}, fmt.Errorf("unsupported sibling %q of reference %q", node.Content[i].Value, ref.Value)
			}
		}
		key, local := strings.CutPrefix(ref.Value, "#/components/"+kind+"/")
		if !local || strings.ContainsRune(key, '/') {
			return yaml.Node{}, fmt.Errorf("unsupported %s reference %q", kind, ref.Value)
		}
		key = strings.NewReplacer("~1", "/", "~0", "~").Replace(key)
		if visited[key] {
			return yaml.Node{}, fmt.Errorf("cyclic %s reference %q", kind, ref.Value)
		}
		var exists bool
		node, exists = definitions[key]
		if !exists {
			return yaml.Node{}, fmt.Errorf("unknown %s reference %q", kind, ref.Value)
		}
		visited[key] = true
	}
}

func (defs componentDefinitions) queryParameter(parameter parameterSpec) (queryParameter, error) {
	if parameter.Content.Kind != 0 {
		return queryParameter{}, fmt.Errorf("content serialization is unsupported")
	}
	if (parameter.Style != "" && parameter.Style != "form") || (parameter.Explode != nil && !*parameter.Explode) {
		return queryParameter{}, fmt.Errorf("query serialization must use exploded form")
	}
	schema, err := resolveQueryReference(parameter.Schema, "schemas", defs.Schemas)
	if err != nil {
		return queryParameter{}, err
	}
	if scalarValue(&schema, "type") == "array" {
		return defs.arrayQueryParameter(parameter, schema)
	}
	result, err := scalarQueryParameter(schema)
	result.Name = parameter.Name
	result.Required = parameter.Required
	return result, err
}

func (defs componentDefinitions) arrayQueryParameter(parameter parameterSpec, schema yaml.Node) (queryParameter, error) {
	if err := validateQuerySchemaFields(schema, []string{"items", "minItems", "maxItems"}); err != nil {
		return queryParameter{}, err
	}
	var array struct {
		Items    yaml.Node `yaml:"items"`
		MinItems *uint     `yaml:"minItems"`
		MaxItems *uint     `yaml:"maxItems"`
	}
	if err := schema.Decode(&array); err != nil {
		return queryParameter{}, err
	}
	item, err := resolveQueryReference(array.Items, "schemas", defs.Schemas)
	if err != nil {
		return queryParameter{}, err
	}
	result, err := scalarQueryParameter(item)
	result.Name = parameter.Name
	result.Required = parameter.Required
	result.Repeatable = true
	result.MinItems = array.MinItems
	result.MaxItems = array.MaxItems
	return result, err
}

func scalarQueryParameter(schema yaml.Node) (queryParameter, error) {
	var result queryParameter
	kind := scalarValue(&schema, "type")
	switch kind {
	case "integer", "number":
		if err := validateQuerySchemaFields(schema, []string{"enum", "minimum", "maximum"}); err != nil {
			return result, err
		}
	case "string":
		if err := validateQuerySchemaFields(schema, []string{"enum", "minLength", "maxLength"}); err != nil {
			return result, err
		}
	case "boolean":
		if err := validateQuerySchemaFields(schema, []string{"enum"}); err != nil {
			return result, err
		}
	default:
		return result, fmt.Errorf("unsupported query schema type %q", kind)
	}
	if err := schema.Decode(&result); err != nil {
		return result, err
	}
	for _, bound := range []*float64{result.Minimum, result.Maximum} {
		if bound != nil && (math.IsInf(*bound, 0) || math.IsNaN(*bound)) {
			return result, fmt.Errorf("numeric bounds must be finite")
		}
	}
	result.Type = kind
	values, err := queryEnum(mappingValue(&schema, "enum"), kind)
	result.Enum = values
	return result, err
}

func validateQuerySchemaFields(schema yaml.Node, fields []string) error {
	for i := 0; i < len(schema.Content); i += 2 {
		key := schema.Content[i].Value
		switch key {
		case "type", "description", "title", "default", "example", "examples", "deprecated", "format", "pattern":
			continue
		}
		if !slices.Contains(fields, key) && !strings.HasPrefix(key, "x-") {
			return fmt.Errorf("unsupported query schema field %q", key)
		}
	}
	return nil
}

func queryEnum(node *yaml.Node, kind string) ([]any, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return nil, fmt.Errorf("enum requires a nonempty sequence")
	}
	var values []any
	for _, item := range node.Content {
		var value any
		if err := item.Decode(&value); err != nil {
			return nil, err
		}
		valid := false
		switch item.Tag {
		case "!!int":
			valid = kind == "integer" || kind == "number"
		case "!!float":
			number, ok := value.(float64)
			valid = kind == "number" && ok && !math.IsNaN(number) && !math.IsInf(number, 0)
		case "!!str":
			valid = kind == "string"
		case "!!bool":
			valid = kind == "boolean"
		}
		if !valid {
			return nil, fmt.Errorf("enum value %q is not a %s", item.Value, kind)
		}
		values = append(values, value)
	}
	return values, nil
}
