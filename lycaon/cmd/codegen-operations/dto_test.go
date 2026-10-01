package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDTOGenerationPreservesWirePresenceAndRepresentations(t *testing.T) {
	t.Parallel()
	source := []byte(`components:
  schemas:
    Edit:
      type: object
      x-go-generate: true
      x-go-field-order: [enabled, payload, at, entries]
      required: [at, entries]
      properties:
        at: {type: string, format: date-time}
        enabled: {type: boolean, x-go-pointer: true}
        payload: {x-go-type: json.RawMessage}
        entries: {type: array, items: {type: string}}
`)
	generated, err := renderGoDTOs(source)
	testutil.FailErr(t, "generate DTO", err)
	file, err := parser.ParseFile(token.NewFileSet(), "types.go", generated, 0)
	testutil.FailErr(t, "parse generated DTO", err)
	var fields []*ast.Field
	ast.Inspect(file, func(node ast.Node) bool {
		if structure, ok := node.(*ast.StructType); ok {
			fields = structure.Fields.List
		}
		return true
	})
	if len(fields) != 4 {
		t.Fatalf("got %d fields", len(fields))
	}
	names := []string{"Enabled", "Payload", "At", "Entries"}
	tags := []string{`json:"enabled,omitempty"`, `json:"payload,omitempty"`, `json:"at"`, `json:"entries"`}
	for i, field := range fields {
		tag, err := strconv.Unquote(field.Tag.Value)
		testutil.FailErr(t, "read generated tag", err)
		if field.Names[0].Name != names[i] || tag != tags[i] {
			t.Fatalf("field %d: %s %s", i, field.Names[0], tag)
		}
	}
	if pointer, ok := fields[0].Type.(*ast.StarExpr); !ok || pointer.X.(*ast.Ident).Name != "bool" {
		t.Fatal("optional edit must preserve explicit false")
	}
	if selector, ok := fields[1].Type.(*ast.SelectorExpr); !ok || selector.Sel.Name != "RawMessage" {
		t.Fatal("payload must preserve its JSON representation")
	}
	if selector, ok := fields[2].Type.(*ast.SelectorExpr); !ok || selector.Sel.Name != "Time" {
		t.Fatal("date-time must use time.Time")
	}
	if array, ok := fields[3].Type.(*ast.ArrayType); !ok || array.Len != nil {
		t.Fatal("entries must remain a slice")
	}
}

func TestDTOGenerationRejectsAmbiguousSchemas(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, schema, errorText string }{
		{"union", "type: object\nproperties: {value: {oneOf: [{type: string}, {type: integer}]}}", "explicit Go representation"},
		{"inline object", "type: object\nproperties: {value: {type: object, properties: {nested: {type: string}}}}", "named schema"},
		{"missing items", "type: object\nproperties: {value: {type: array}}", "array items"},
		{"field collision", "type: object\nproperties: {id: {type: string}, ID: {type: string}}", "duplicate Go field"},
		{"missing required", "type: object\nrequired: [missing]", "required property"},
		{"repeated order", "type: object\nx-go-field-order: [a, a]\nproperties: {a: {type: string}, b: {type: string}}", "repeated Go field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := "components:\n  schemas:\n    Example:\n      x-go-generate: true\n      " + strings.ReplaceAll(tc.schema, "\n", "\n      ") + "\n"
			_, err := renderGoDTOs([]byte(source))
			if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("got %v, want %q", err, tc.errorText)
			}
		})
	}
}

func TestDTODescriptionsDoNotCreateImports(t *testing.T) {
	t.Parallel()
	generated, err := renderGoDTOs([]byte(`components:
  schemas:
    Label:
      x-go-generate: true
      type: object
      description: Describes a time.Time and json.RawMessage without carrying either.
      properties:
        name: {type: string}
`))
	testutil.FailErr(t, "generate described DTO", err)
	file, err := parser.ParseFile(token.NewFileSet(), "types.go", generated, 0)
	testutil.FailErr(t, "parse described DTO", err)
	if len(file.Imports) != 0 {
		t.Fatalf("descriptions created imports: %v", file.Imports)
	}
}

func TestDTOSchemaGoNameRenamesTypeAndReferences(t *testing.T) {
	t.Parallel()
	generated, err := renderGoDTOs([]byte(`components:
  schemas:
    Error:
      x-go-generate: true
      x-go-name: ErrorResponse
      type: object
      properties:
        code: {type: string}
    BatchRow:
      x-go-generate: true
      type: object
      properties:
        failure: {$ref: '#/components/schemas/Error'}
`))
	testutil.FailErr(t, "generate renamed DTO", err)
	source := string(generated)
	for _, want := range []string{"type ErrorResponse struct", "Failure ErrorResponse `json:\"failure,omitempty\"`"} {
		if !strings.Contains(source, want) {
			t.Fatalf("generated source lacks %q:\n%s", want, source)
		}
	}
	if strings.Contains(source, "type Error struct") {
		t.Fatalf("schema name leaked as the Go type:\n%s", source)
	}
}
