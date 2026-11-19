package argdiag_test

import (
	"slices"
	"testing"
)

func TestUnsupportedPropertyRemedyUsesExactUnconditionalPaths(t *testing.T) {
	closedObject := map[string]any{"type": "object", "additionalProperties": false}
	args := map[string]any{"options": map[string]any{"draft/~": true}}
	for _, tc := range []struct {
		name     string
		property map[string]any
		want     []string
	}{
		{name: "closed nested object", property: closedObject, want: []string{`"/options/draft~1~0"`}},
		{name: "mutually exclusive shapes", property: map[string]any{"oneOf": []any{closedObject, map[string]any{"type": "string"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := map[string]any{"type": "object", "properties": map[string]any{"options": tc.property}}
			details := rejectArgs(t, schema, args)
			paths, _ := details["unsupported_property_paths"].([]string)
			if !slices.Equal(paths, tc.want) {
				t.Fatalf("unsupported paths = %v, want %v", paths, tc.want)
			}
		})
	}
}
