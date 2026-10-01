package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPatchUnitSchemaDescriptionReplacesFoldedScalar(t *testing.T) {
	t.Parallel()
	input := `description: >-
  Stage and commit paths with a message.
  Replaces command: git commit.
schema:
  type: object
`
	got, err := patchUnitSchemaDescription(input, " Replaces command: git add paths && git commit -m.")
	if err != nil {
		t.Fatalf("patch description: %v", err)
	}
	if strings.Contains(got, "\n  Stage and commit") {
		t.Fatalf("folded scalar continuation remained after replacement:\n%s", got)
	}
	var unit struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(got), &unit); err != nil {
		t.Fatalf("parse patched YAML: %v", err)
	}
	want := "Stage and commit paths with a message. Replaces command: git add paths && git commit -m."
	if unit.Description != want {
		t.Fatalf("description = %q, want %q", unit.Description, want)
	}
}

func TestRenderGenPrefersFirstClassReplacementExample(t *testing.T) {
	t.Parallel()
	got, err := renderGenGo(&equivalenceFile{Entries: []equivalenceEntry{{
		Native:             "http_request",
		Replaces:           []replaceHabit{{Habit: "ad hoc HTTP request", Examples: []string{"shell-client fixture"}}},
		ReplacementExample: `http_request {"url":"https://api.example.test"}`,
		RedirectCode:       "USE_HTTP_REQUEST_NATIVE",
	}}})
	if err != nil {
		t.Fatalf("render generated command equivalence: %v", err)
	}
	text := string(got)
	if !strings.Contains(text, `Example:      "http_request {\"url\":\"https://api.example.test\"}"`) {
		t.Fatalf("generated output missing first-class replacement example:\n%s", text)
	}
	if strings.Contains(text, "shell-client fixture") {
		t.Fatalf("generated output leaked parser fixture into agent feedback:\n%s", text)
	}
}

func TestPatchUnitSchemaDescriptionQuotedContinuation(t *testing.T) {
	t.Parallel()
	input := "description: 'Rewrite one\n  shape.'\nschema:\n  type: object\n"
	got, err := patchUnitSchemaDescription(input, " Replaces command: rewrite.")
	if err != nil {
		t.Fatalf("patch description: %v", err)
	}
	var unit struct {
		Description string
		Schema      map[string]any
	}
	if err := yaml.Unmarshal([]byte(got), &unit); err != nil {
		t.Fatalf("parse patched schema: %v", err)
	}
	if unit.Description != "Rewrite one shape. Replaces command: rewrite." || unit.Schema["type"] != "object" {
		t.Fatalf("patched unit = %#v", unit)
	}
	repeated, err := patchUnitSchemaDescription(got, " Replaces command: rewrite.")
	if err != nil || repeated != got {
		t.Fatalf("patch must be idempotent: %v; %q", err, repeated)
	}
}

func TestPatchUnitSchemaDescriptionRefusesInvalidInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"description: 'unfinished", "schema: {}", "description: []", "[]"} {
		if _, err := patchUnitSchemaDescription(input, " Replaces command: read."); err == nil {
			t.Errorf("accepted invalid input %q", input)
		}
	}
}
