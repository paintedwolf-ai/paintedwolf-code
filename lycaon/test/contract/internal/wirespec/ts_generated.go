package wirespec

import (
	"fmt"
	"regexp"
	"strings"
)

// extractGeneratedSchemaObjectBody returns the interior of a named object schema
// from openapi-typescript output (components.schemas members at 8-space indent).
// Generated types only — no hand `export interface` form.
func extractGeneratedSchemaObjectBody(content, name string) (string, error) {
	needle := "        " + name + ": {"
	idx := strings.Index(content, "\n"+needle)
	var openBrace int
	switch {
	case idx >= 0:
		openBrace = idx + 1 + len(needle) - 1
	case strings.HasPrefix(content, needle):
		openBrace = len(needle) - 1
	case strings.Contains(content, "\n        "+name+": Record<string, never>;"):
		// An empty object renders as Record<string, never>, not a body.
		return "", nil
	default:
		return "", fmt.Errorf("generated schema object %q not found in types.ts", name)
	}
	return braceInterior(content, openBrace)
}

func braceInterior(content string, openBrace int) (string, error) {
	if openBrace < 0 || openBrace >= len(content) || content[openBrace] != '{' {
		return "", fmt.Errorf("generated schema: expected '{' at open")
	}
	depth := 0
	for i := openBrace; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[openBrace+1 : i], nil
			}
		}
	}
	return "", fmt.Errorf("generated schema: unclosed object")
}

func tsGeneratedSchemaExists(content, name string) bool {
	needle := "        " + name + ": {"
	if strings.Contains(content, "\n"+needle) || strings.HasPrefix(content, needle) {
		return true
	}
	flat := `export type ` + name + ` = components["schemas"]["` + name + `"]`
	return strings.Contains(content, flat)
}

// generatedSchemaFieldRe matches a wire field line in a generated object schema.
// Field names are already snake_case on the OpenAPI wire.
var generatedSchemaFieldRe = regexp.MustCompile(`(?m)^            (?:readonly )?(\w+)(\?)?:`)

func parseGeneratedSchemaFieldSpecs(content, name string) ([]wireField, error) {
	body, err := extractGeneratedSchemaObjectBody(content, name)
	if err != nil {
		return nil, err
	}
	var out []wireField
	for _, fm := range generatedSchemaFieldRe.FindAllStringSubmatch(body, -1) {
		out = append(out, wireField{
			Name:     fm[1],
			Optional: fm[2] == "?",
		})
	}
	return out, nil
}

func parseGeneratedSchemaFieldNames(content, name string) ([]string, error) {
	fields, err := parseGeneratedSchemaFieldSpecs(content, name)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Name
	}
	return out, nil
}
