package bindings

import (
	"fmt"
	"regexp"
)

// FieldType is the closed set of projected field types.
type FieldType string

const (
	TypeBool   FieldType = "bool"
	TypeString FieldType = "string"
	TypeInt    FieldType = "int"
)

// FieldSpec projects one JSON Pointer path into a typed store key.
type FieldSpec struct {
	Key    string    `yaml:"key"`
	Type   FieldType `yaml:"type"`
	Path   string    `yaml:"path"`
	Equals string    `yaml:"equals,omitempty"`
}

// Binding is one pack mcp_bindings/<id>.yaml document.
type Binding struct {
	ID         string         `yaml:"id"`
	ProviderID string         `yaml:"provider_id"`
	ToolName   string         `yaml:"tool_name"`
	Schema     map[string]any `yaml:"schema"`
	Fields     []FieldSpec    `yaml:"fields"`

	// compiled is set at load; not serialized.
	compiled any // *jsonschema.Schema — typed in load.go to avoid exporting
}

// FieldStore holds only bool / string / int values keyed by FieldSpec.Key.
type FieldStore map[string]any

var fieldKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateFieldKey(key string) error {
	if !fieldKeyRE.MatchString(key) {
		return fmt.Errorf("invalid field key %q (want [a-z][a-z0-9_]*)", key)
	}
	return nil
}

func validateFieldType(t FieldType) error {
	switch t {
	case TypeBool, TypeString, TypeInt:
		return nil
	default:
		return fmt.Errorf("unknown field type %q (want bool|string|int)", t)
	}
}
