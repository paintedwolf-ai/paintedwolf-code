package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func scalarValue(node *yaml.Node, key string) string {
	value := mappingValue(node, key)
	if value == nil {
		return ""
	}
	return value.Value
}

func stringList(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	var result []string
	for _, value := range node.Content {
		result = append(result, value.Value)
	}
	return result
}

func renderGoDTOs(raw []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 {
		return nil, fmt.Errorf("expected one OpenAPI document")
	}
	schemas := mappingValue(mappingValue(document.Content[0], "components"), "schemas")
	if schemas == nil {
		return nil, fmt.Errorf("OpenAPI schemas are missing")
	}
	selected := make(map[string]*yaml.Node)
	typeNames := make(dtoTypeNames)
	var names []string
	for i := 0; i+1 < len(schemas.Content); i += 2 {
		schema := schemas.Content[i+1]
		name := schemas.Content[i].Value
		if goName := scalarValue(schema, "x-go-name"); goName != "" {
			typeNames[name] = goName
		}
		if scalarValue(schema, "x-go-generate") != "true" {
			continue
		}
		selected[name] = schema
		names = append(names, name)
	}
	sort.Strings(names)
	var body bytes.Buffer
	for _, name := range names {
		if err := typeNames.renderGoDTO(&body, name, selected[name]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	dependencies, err := dtoImports(body.Bytes())
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("// Code generated from docs/openapi.yaml. DO NOT EDIT.\n\npackage api\n\n")
	for _, dependency := range dependencies {
		fmt.Fprintf(&out, "import %q\n", dependency)
	}
	out.Write(body.Bytes())
	return format.Source(out.Bytes())
}

func dtoImports(body []byte) ([]string, error) {
	source := append([]byte("package api\n"), body...)
	file, err := parser.ParseFile(token.NewFileSet(), "types.go", source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	needed := make(map[string]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if qualifier, ok := selector.X.(*ast.Ident); ok {
			needed[qualifier.Name] = true
		}
		return true
	})
	var imports []string
	for _, dependency := range []struct{ name, path string }{{"json", "encoding/json"}, {"time", "time"}} {
		if needed[dependency.name] {
			imports = append(imports, dependency.path)
		}
	}
	return imports, nil
}

// dtoTypeNames maps a schema to the Go type name its x-go-name declares.
type dtoTypeNames map[string]string

func (n dtoTypeNames) goTypeName(schema string) string {
	if goName, ok := n[schema]; ok {
		return goName
	}
	return schema
}

func (n dtoTypeNames) renderGoDTO(out *bytes.Buffer, schemaName string, schema *yaml.Node) error {
	name := n.goTypeName(schemaName)
	if !validExportedName(name) {
		return fmt.Errorf("invalid Go type name %q", name)
	}
	if scalarValue(schema, "type") != "object" {
		return fmt.Errorf("generated DTO must be an object")
	}
	properties := mappingValue(schema, "properties")
	fields := make(map[string]*yaml.Node)
	var order []string
	if properties != nil {
		for i := 0; i+1 < len(properties.Content); i += 2 {
			key := properties.Content[i].Value
			fields[key] = properties.Content[i+1]
			order = append(order, key)
		}
	}
	if explicit := mappingValue(schema, "x-go-field-order"); explicit != nil {
		order = stringList(explicit)
	}
	if len(order) != len(fields) {
		return fmt.Errorf("field order must name each property once")
	}
	required := make(map[string]bool)
	for _, key := range stringList(mappingValue(schema, "required")) {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("required property %q is missing", key)
		}
		required[key] = true
	}
	writeGoDescription(out, name, scalarValue(schema, "description"))
	fmt.Fprintf(out, "type %s struct {\n", name)
	seen := make(map[string]bool)
	goNames := make(map[string]bool)
	for _, key := range order {
		field, ok := fields[key]
		if !ok || seen[key] {
			return fmt.Errorf("invalid or repeated Go field %q", key)
		}
		seen[key] = true
		fieldName := scalarValue(field, "x-go-name")
		if fieldName == "" {
			fieldName = goFieldName(key)
		}
		if !validExportedName(fieldName) {
			return fmt.Errorf("invalid Go field name %q", fieldName)
		}
		if goNames[fieldName] {
			return fmt.Errorf("duplicate Go field name %q", fieldName)
		}
		goNames[fieldName] = true
		fieldType, err := n.goSchemaType(field)
		if err != nil {
			return fmt.Errorf("property %s: %w", key, err)
		}
		tag := key
		if !required[key] {
			tag += ",omitempty"
		}
		writeGoDescription(out, "", scalarValue(field, "description"))
		fmt.Fprintf(out, "%s %s `json:%s`\n", fieldName, fieldType, strconv.Quote(tag))
	}
	out.WriteString("}\n\n")
	return nil
}

func writeGoDescription(out *bytes.Buffer, name, description string) {
	text := strings.TrimSpace(description)
	if name != "" {
		text = strings.TrimSpace(name + " " + text)
	}
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(out, "// %s\n", strings.TrimSpace(line))
	}
}

func validExportedName(name string) bool {
	expr, err := parser.ParseExpr(name)
	ident, ok := expr.(*ast.Ident)
	return err == nil && ok && ident.Name == name && len(name) > 0 && unicode.IsUpper([]rune(name)[0])
}

func goFieldName(key string) string {
	initialisms := map[string]bool{}
	for _, name := range strings.Fields("API CPU CSS DNS EOF GUID HTML HTTP HTTPS ID IP JSON LLM MCP RAM RPC SARIF SSE SQL SSH TCP TLS TS UI UID URI URL UTF UUID XML XSS YAML EOL SHA SHA256 JWT") {
		initialisms[name] = true
	}
	parts := strings.Split(key, "_")
	for i, part := range parts {
		if initialisms[strings.ToUpper(part)] {
			parts[i] = strings.ToUpper(part)
		} else if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}

func (n dtoTypeNames) goSchemaType(schema *yaml.Node) (string, error) {
	if override := scalarValue(schema, "x-go-type"); override != "" {
		if _, err := parser.ParseExpr(override); err != nil {
			return "", fmt.Errorf("invalid Go type: %w", err)
		}
		return override, nil
	}
	value, err := n.goSchemaValueType(schema)
	if err != nil {
		return "", err
	}
	nullable := false
	typ := mappingValue(schema, "type")
	if typ != nil && typ.Kind == yaml.SequenceNode {
		for _, t := range typ.Content {
			nullable = nullable || t.Value == "null"
		}
	}
	if scalarValue(schema, "x-go-pointer") == "true" || (nullable && !strings.HasPrefix(value, "[]") && !strings.HasPrefix(value, "map[") && value != "any") {
		value = "*" + value
	}
	return value, nil
}

func (n dtoTypeNames) goSchemaValueType(schema *yaml.Node) (string, error) {
	if ref := scalarValue(schema, "$ref"); ref != "" {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(ref, prefix) {
			return "", fmt.Errorf("unsupported reference %q", ref)
		}
		return n.goTypeName(strings.TrimPrefix(ref, prefix)), nil
	}
	for _, union := range []string{"oneOf", "anyOf", "allOf"} {
		if mappingValue(schema, union) != nil {
			return "", fmt.Errorf("%s needs an explicit Go representation", union)
		}
	}
	kind := scalarValue(schema, "type")
	if node := mappingValue(schema, "type"); node != nil && node.Kind == yaml.SequenceNode {
		for _, t := range node.Content {
			if t.Value != "null" {
				if kind != "" {
					return "", fmt.Errorf("multiple non-null types need an explicit Go representation")
				}
				kind = t.Value
			}
		}
	}
	switch kind {
	case "string":
		if scalarValue(schema, "format") == "date-time" {
			return "time.Time", nil
		}
		return "string", nil
	case "integer":
		if scalarValue(schema, "format") == "int64" {
			return "int64", nil
		}
		return "int", nil
	case "number":
		return "float64", nil
	case "boolean":
		return "bool", nil
	case "array":
		items := mappingValue(schema, "items")
		if items == nil {
			return "", fmt.Errorf("array items are missing")
		}
		element, err := n.goSchemaType(items)
		return "[]" + element, err
	case "object":
		if properties := mappingValue(schema, "properties"); properties != nil && len(properties.Content) > 0 {
			return "", fmt.Errorf("inline object needs a named schema or explicit Go representation")
		}
		additional := mappingValue(schema, "additionalProperties")
		if additional == nil || additional.Value == "true" {
			return "map[string]any", nil
		}
		if additional.Value == "false" {
			return "", fmt.Errorf("closed object needs an explicit Go representation")
		}
		element, err := n.goSchemaType(additional)
		return "map[string]" + element, err
	case "":
		if len(schema.Content) == 0 {
			return "any", nil
		}
	}
	return "", fmt.Errorf("unsupported schema type %q", kind)
}
