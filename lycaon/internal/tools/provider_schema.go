package tools

import "fmt"

// Function parameter roots exclude relational constraints.
var functionParametersRootForbiddenKeys = []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"}

// ValidateFunctionParametersRoot checks the function-parameters object after
// ProjectProviderToolMeta.
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
