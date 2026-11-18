package specsync

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var (
	rxSnakeCase      = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	rxLowerCamelCase = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
	rxPathTemplate   = regexp.MustCompile(`\{([^}]+)\}`)
)

type invariantOpenAPIDoc struct {
	Security   []any                     `yaml:"security"`
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Parameters map[string]map[string]any `yaml:"parameters"`
		Responses  map[string]map[string]any `yaml:"responses"`
		Schemas    map[string]map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// invariantDoc is the generic view of the OpenAPI document the convention
// and invariant invariants walk, decoded once per corpus.
func invariantDoc(r *invariants.Run) *invariantOpenAPIDoc {
	return invariants.Part(r.Corpus, "openapi document", func() (*invariantOpenAPIDoc, error) {
		var doc invariantOpenAPIDoc
		if err := spec(r).Decode(&doc); err != nil {
			return nil, err
		}
		if len(doc.Paths) == 0 {
			return nil, fmt.Errorf("%s declares no paths", openAPIPath)
		}
		return &doc, nil
	})
}

func resolveParamMap(p map[string]any, compParams map[string]map[string]any) map[string]any {
	if ref, ok := p["$ref"].(string); ok {
		refKey := strings.TrimPrefix(ref, "#/components/parameters/")
		if resolved, found := compParams[refKey]; found {
			return resolved
		}
	}
	return p
}

// openAPIPathParameterInvariants verifies path parameter naming and template-parameter parity.
func openAPIPathParameterInvariants(r *invariants.Run) {
	doc := invariantDoc(r)

	forbiddenBareNouns := map[string]bool{
		"job":     true,
		"project": true,
		"session": true,
		"entry":   true,
		"secret":  true,
	}

	for path, methods := range doc.Paths {
		pathParams := rxPathTemplate.FindAllStringSubmatch(path, -1)
		expectedInPath := make(map[string]bool, len(pathParams))

		for _, match := range pathParams {
			paramName := match[1]
			expectedInPath[paramName] = true

			if !rxSnakeCase.MatchString(paramName) {
				r.Report("%s: path parameter %q must be lowercase snake_case", path, paramName)
			}
			if forbiddenBareNouns[paramName] {
				r.Report("%s: path parameter %q is a bare noun; use %s_id", path, paramName, paramName)
			}
		}

		pathLevelParams := asSlice(methods["parameters"])

		for method, rawOp := range methods {
			methodUpper := strings.ToUpper(method)
			if methodUpper == "PARAMETERS" || methodUpper == "SERVERS" {
				continue
			}

			opMap := asMap(rawOp)
			if opMap == nil {
				continue
			}

			declaredPathParams := make(map[string]bool)
			allParams := append(asSlice(opMap["parameters"]), pathLevelParams...)

			for _, rawParam := range allParams {
				paramMap := asMap(rawParam)
				if paramMap == nil {
					continue
				}
				resolved := resolveParamMap(paramMap, doc.Components.Parameters)
				if asString(resolved["in"]) == "path" {
					name := asString(resolved["name"])
					declaredPathParams[name] = true
					if !expectedInPath[name] {
						r.Report("%s %s: declared path parameter %q does not exist in URL template", methodUpper, path, name)
					}
					if !asBool(resolved["required"]) {
						r.Report("%s %s: path parameter %q must be required: true", methodUpper, path, name)
					}
				}
			}

			for p := range expectedInPath {
				if !declaredPathParams[p] {
					r.Report("%s %s: template parameter {%s} is missing from operation parameter declarations", methodUpper, path, p)
				}
			}
		}
	}
}

// openAPIQueryParameterInvariants verifies query parameter naming, scoping, and search conventions.
func openAPIQueryParameterInvariants(r *invariants.Run) {
	doc := invariantDoc(r)

	forbiddenScoping := map[string]string{
		"project":     "project_id",
		"project_dir": "project_id",
		"session":     "session_id",
	}

	forbiddenSearch := map[string]string{
		"query":  "q",
		"search": "q",
		"term":   "q",
	}

	for path, methods := range doc.Paths {
		for method, rawOp := range methods {
			methodUpper := strings.ToUpper(method)
			if methodUpper == "PARAMETERS" || methodUpper == "SERVERS" {
				continue
			}

			opMap := asMap(rawOp)
			if opMap == nil {
				continue
			}

			for _, rawParam := range asSlice(opMap["parameters"]) {
				paramMap := asMap(rawParam)
				if paramMap == nil {
					continue
				}
				resolved := resolveParamMap(paramMap, doc.Components.Parameters)
				if asString(resolved["in"]) != "query" {
					continue
				}

				name := asString(resolved["name"])
				if name == "" {
					continue
				}

				if !rxSnakeCase.MatchString(name) {
					r.Report("%s %s: query parameter %q must be lowercase snake_case", methodUpper, path, name)
				}
				if canonical, bad := forbiddenScoping[name]; bad {
					r.Report("%s %s: query parameter %q is forbidden; use canonical %q", methodUpper, path, name, canonical)
				}
				if canonical, bad := forbiddenSearch[name]; bad {
					r.Report("%s %s: query parameter %q is forbidden; use canonical %q", methodUpper, path, name, canonical)
				}
			}
		}
	}
}

// openAPIStatusCodeAndBodyInvariants verifies response status codes and body shapes.
func openAPIStatusCodeAndBodyInvariants(r *invariants.Run) {
	doc := invariantDoc(r)

	for path, methods := range doc.Paths {
		for method, rawOp := range methods {
			methodUpper := strings.ToUpper(method)
			if methodUpper == "PARAMETERS" || methodUpper == "SERVERS" {
				continue
			}

			opMap := asMap(rawOp)
			if opMap == nil {
				continue
			}

			responses := asMap(opMap["responses"])
			if responses == nil {
				continue
			}

			if resp204 := asMap(responses["204"]); resp204 != nil {
				if content := asMap(resp204["content"]); len(content) > 0 {
					r.Report("%s %s: status 204 No Content must not define a response body", methodUpper, path)
				}
			}

			if resp201 := asMap(responses["201"]); resp201 != nil {
				content := asMap(resp201["content"])
				ref := asString(resp201["$ref"])
				if len(content) == 0 && ref == "" {
					r.Report("%s %s: status 201 Created must define a response body schema", methodUpper, path)
				}
			}
		}
	}
}

// openAPISchemaPropertiesInvariants verifies schema property casing.
func openAPISchemaPropertiesInvariants(r *invariants.Run) {
	doc := invariantDoc(r)

	for schemaName, rawSchema := range doc.Components.Schemas {
		schemaMap := asMap(rawSchema)
		if schemaMap == nil {
			continue
		}
		props := asMap(schemaMap["properties"])
		for propName := range props {
			if !rxSnakeCase.MatchString(propName) {
				r.Report("schema %s: property %q must be lowercase snake_case", schemaName, propName)
			}
		}
	}
}

// openAPIOperationIDInvariants verifies operationId presence, format, and uniqueness.
func openAPIOperationIDInvariants(r *invariants.Run) {
	doc := invariantDoc(r)

	seenIDs := make(map[string]string)

	for path, methods := range doc.Paths {
		for method, rawOp := range methods {
			methodUpper := strings.ToUpper(method)
			if methodUpper == "PARAMETERS" || methodUpper == "SERVERS" {
				continue
			}

			opMap := asMap(rawOp)
			if opMap == nil {
				continue
			}

			opID := asString(opMap["operationId"])
			if opID == "" {
				r.Report("%s %s: missing operationId", methodUpper, path)
				continue
			}

			if !rxLowerCamelCase.MatchString(opID) {
				r.Report("%s %s: operationId %q must be lowerCamelCase", methodUpper, path, opID)
			}

			route := methodUpper + " " + path
			if prior, exists := seenIDs[opID]; exists {
				r.Report("duplicate operationId %q on %s (first seen on %s)", opID, route, prior)
			}
			seenIDs[opID] = route
		}
	}
}

var invariantRules = []invariants.Invariant{
	{Name: "openapi-path-parameters", Anchors: []invariants.Anchor{openAPI}, Check: openAPIPathParameterInvariants},
	{Name: "openapi-query-parameters", Anchors: []invariants.Anchor{openAPI}, Check: openAPIQueryParameterInvariants},
	{Name: "openapi-status-codes-and-bodies", Anchors: []invariants.Anchor{openAPI}, Check: openAPIStatusCodeAndBodyInvariants},
	{Name: "openapi-property-casing", Anchors: []invariants.Anchor{openAPI}, Check: openAPISchemaPropertiesInvariants},
	{Name: "openapi-operation-ids", Anchors: []invariants.Anchor{openAPI}, Check: openAPIOperationIDInvariants},
}
