package filebriefing

import (
	"reflect"
	"testing"
)

func TestParseSectionsKeepsExplanationMarkdown(t *testing.T) {
	got := ParseSections(`- Purpose: Coordinates request routing
- Structure: Connects the bounded cache to ` + "`dispatch`" + `
- Key behavior: Cancels superseded work`)
	want := []Section{
		{Kind: "purpose", Text: "Coordinates request routing"},
		{Kind: "structure", Text: "Connects the bounded cache to `dispatch`"},
		{Kind: "key_behavior", Text: "Cancels superseded work"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sections = %#v, want %#v", got, want)
	}
}

func TestParseSectionsRejectsUnstructuredProse(t *testing.T) {
	if got := ParseSections("This file probably coordinates requests."); len(got) != 0 {
		t.Fatalf("sections = %#v, want none", got)
	}
}

func TestParseSectionsRejectsIncompleteAndDuplicateShapes(t *testing.T) {
	for _, summary := range []string{
		"Purpose: Coordinates requests\nStructure: Routes handlers",
		"Purpose: Coordinates requests\nPurpose: Routes handlers\nKey behavior: Cancels work",
		"Purpose:\nPurpose: Coordinates requests\nStructure: Routes handlers\nKey behavior: Cancels work",
	} {
		if got := ParseSections(summary); len(got) != 0 {
			t.Fatalf("sections = %#v, want none", got)
		}
	}
}

func TestParseSectionsAcceptsMarkdownLabelsAndWrappedBodies(t *testing.T) {
	want := []Section{
		{Kind: "purpose", Text: "Coordinates request routing"},
		{Kind: "structure", Text: "Connects the bounded cache to request dispatch"},
		{Kind: "key_behavior", Text: "Cancels superseded work"},
	}
	wrapped := ParseSections(`- **Purpose:**
Coordinates request routing
- **Structure:**
Connects the bounded cache to request dispatch
- **Key behavior:**
Cancels superseded work`)
	if !reflect.DeepEqual(wrapped, want) {
		t.Fatalf("wrapped sections = %#v, want %#v", wrapped, want)
	}
	sameLine := ParseSections(`- **Purpose:** Coordinates request routing
- **Structure:** Connects the bounded cache to request dispatch
- **Key behavior:** Cancels superseded work`)
	if !reflect.DeepEqual(sameLine, want) {
		t.Fatalf("same-line sections = %#v, want %#v", sameLine, want)
	}
}
