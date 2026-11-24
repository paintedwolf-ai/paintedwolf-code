package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestOperationQueryGeneration(t *testing.T) {
	t.Parallel()
	const source = `
paths:
  /v1/items/{id}:
    parameters:
      - {in: path, name: id, required: true, schema: {type: string}}
      - {in: query, name: id, schema: {type: string, minLength: 1, maxLength: 10}}
      - {$ref: "#/components/parameters/defaultSize"}
    get:
      operationId: listItems
      parameters:
        - {$ref: "#/components/parameters/listSize"}
        - {in: query, name: active, schema: {type: boolean, enum: [true]}}
        - {in: query, name: weight, schema: {type: number, minimum: -1.5, maximum: 2.5, enum: [-1.5, 0, 2.5]}}
        - in: query
          name: filters
          style: form
          explode: true
          schema: {type: array, minItems: 1, maxItems: 5, items: {$ref: "#/components/schemas/filter"}}
components:
  parameters:
    defaultSize:
      {in: query, name: size, schema: {type: integer, maximum: 100}}
    listSize:
      {$ref: "#/components/parameters/requiredSize"}
    requiredSize:
      {in: query, name: size, required: true, schema: {type: integer, minimum: 0, maximum: 25, enum: [0, 25]}}
  schemas:
    filter: {$ref: "#/components/schemas/filter~1kind~0", default: first}
    filter/kind~: {type: string, enum: [first, second]}
`
	var spec document
	testutil.FailErr(t, "read fixture", yaml.Unmarshal([]byte(source), &spec))
	operations, err := loadOperations(spec)
	testutil.FailErr(t, "load operations", err)
	out, err := renderTS(operations, "1.0.0")
	testutil.FailErr(t, "render operations", err)
	for _, want := range []string{
		`{"name":"active","type":"boolean","enum":[true]}`,
		`{"name":"filters","type":"string","repeatable":true,"enum":["first","second"],"minItems":1,"maxItems":5}`,
		`{"name":"id","type":"string","minLength":1,"maxLength":10}`,
		`{"name":"size","type":"integer","required":true,"minimum":0,"maximum":25,"enum":[0,25]}`,
		`{"name":"weight","type":"number","minimum":-1.5,"maximum":2.5,"enum":[-1.5,0,2.5]}`,
		`listItems: { id: string | number; };`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %s from operation output", want)
		}
	}
}

func TestOperationQueryRejectsUnsupportedParameters(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, parameter, problem string }{
		{"object", "{in: query, name: value, schema: {type: object}}", `type "object"`},
		{"style", "{in: query, name: value, style: spaceDelimited, schema: {type: string}}", "exploded form"},
		{"explode", "{in: query, name: value, explode: false, schema: {type: array, items: {type: string}}}", "exploded form"},
		{"content", "{in: query, name: value, content: {application/json: {schema: {type: string}}}}", "content serialization"},
		{"oneOf", "{in: query, name: value, schema: {type: string, oneOf: [{type: string}]}}", "oneOf"},
		{"anyOf", "{in: query, name: value, schema: {type: number, anyOf: [{type: number}]}}", "anyOf"},
		{"allOf", "{in: query, name: value, schema: {type: boolean, allOf: [{type: boolean}]}}", "allOf"},
		{"nested array", "{in: query, name: value, schema: {type: array, items: {type: array, items: {type: string}}}}", `type "array"`},
		{"missing items", "{in: query, name: value, schema: {type: array}}", "expected schemas mapping"},
		{"unsupported bound", "{in: query, name: value, schema: {type: integer, exclusiveMinimum: 5}}", "exclusiveMinimum"},
		{"invalid bound", "{in: query, name: value, schema: {type: number, minimum: .inf}}", "finite"},
		{"invalid enum", "{in: query, name: value, schema: {type: integer, enum: [one]}}", "does not match type integer"},
		{"invalid enum shape", "{in: query, name: value, schema: {type: integer, enum: []}}", "nonempty sequence"},
		{"unknown parameter ref", `{$ref: "#/components/parameters/missing"}`, "unknown parameters reference"},
		{"external ref", `{$ref: "foreign.yaml#/parameter"}`, "unsupported parameters reference"},
		{"schema ref cycle", `{in: query, name: value, schema: {$ref: "#/components/schemas/loop"}}`, "cyclic schemas reference"},
		{"schema ref refinement", `{in: query, name: value, schema: {$ref: "#/components/schemas/loop", maxLength: 2}}`, "unsupported sibling"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := fmt.Sprintf(`
paths:
  /v1/items:
    get:
      operationId: listItems
      parameters: [%s]
components:
  schemas:
    loop: {$ref: "#/components/schemas/loop"}
`, tc.parameter)
			var spec document
			testutil.FailErr(t, "read fixture", yaml.Unmarshal([]byte(source), &spec))
			_, err := loadOperations(spec)
			if err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Fatalf("got %v, expected %q", err, tc.problem)
			}
		})
	}
}
