package tools

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolschema"
)

// functionParametersRootForbiddenKeys are JSON Schema keywords the provider
// rejects on the function-parameters object. Nested uses stay.
var functionParametersRootForbiddenKeys = []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"}

// FunctionParametersRootForbiddenKeys returns a copy of the root-keyword ban.
func FunctionParametersRootForbiddenKeys() []string {
	return append([]string(nil), functionParametersRootForbiddenKeys...)
}

// ValidateFunctionParametersRoot checks the function-parameters object after
// TrimCoordinatorToolMeta.
func ValidateFunctionParametersRoot(schema map[string]any) error {
	if schema == nil {
		return fmt.Errorf("function parameters schema is nil")
	}
	typ, _ := schema["type"].(string)
	if typ != "object" {
		return fmt.Errorf("function parameters type %q, want object", typ)
	}
	for _, key := range functionParametersRootForbiddenKeys {
		if _, has := schema[key]; has {
			return fmt.Errorf("function parameters root must not have %s", key)
		}
	}
	return nil
}

// ValidateProviderArgsSchema checks provider projection requirements.
func ValidateProviderArgsSchema(meta ToolMeta) error {
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		return fmt.Errorf("tool name is required")
	}
	if meta.ArgsSchema == nil {
		return fmt.Errorf("tool %q: ArgsSchema is nil", name)
	}
	typ, _ := meta.ArgsSchema["type"].(string)
	if typ != "object" {
		return fmt.Errorf("tool %q: ArgsSchema type %q, want object", name, typ)
	}
	return nil
}

// ValidateRegistryProviderArgsSchemas checks every registered handler exposes a
// provider-safe ArgsSchema on List().
func ValidateRegistryProviderArgsSchemas(reg *DefaultRegistry) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	var violations []string
	for _, meta := range reg.List() {
		if err := ValidateProviderArgsSchema(meta); err != nil {
			violations = append(violations, err.Error())
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return fmt.Errorf("registry tools missing provider args schema:\n  - %s", strings.Join(violations, "\n  - "))
}

// ValidateCatalogSchemaParity checks registered metadata against the catalog.
func ValidateCatalogSchemaParity(reg *DefaultRegistry, cfg *toolschema.Config) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if cfg == nil {
		return fmt.Errorf("tool schemas config required")
	}
	var violations []string
	for name := range cfg.Tools {
		yamlMeta, ok := cfg.ToolMeta(name)
		if !ok || yamlMeta.ArgsSchema == nil {
			continue
		}
		got, registered := reg.Meta(name)
		if !registered {
			continue
		}
		if got.Description != yamlMeta.Description || !reflect.DeepEqual(got.ArgsSchema, yamlMeta.ArgsSchema) {
			violations = append(violations, fmt.Sprintf("tool %q metadata differs from catalog", name))
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return fmt.Errorf("registry metadata differs from catalog:\n  - %s", strings.Join(violations, "\n  - "))
}

// ValidatePromptToolMetas checks a prompt-visible tool surface (ListForPrompt output).
func ValidatePromptToolMetas(label string, metas []ToolMeta) error {
	var violations []string
	for _, meta := range metas {
		if err := ValidateProviderArgsSchema(meta); err != nil {
			violations = append(violations, fmt.Sprintf("%s: %v", label, err))
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return fmt.Errorf("prompt tool surface missing provider args schema:\n  - %s", strings.Join(violations, "\n  - "))
}
