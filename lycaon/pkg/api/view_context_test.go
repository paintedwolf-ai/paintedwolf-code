package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestViewContextMapTextVisitsEveryTextFieldOnly(t *testing.T) {
	location := func(path string) *ViewLocation {
		return &ViewLocation{RootID: "r", Path: path, DisplayPath: "@d/" + path, Line: 7}
	}
	view := ViewContext{
		Editors:  []ViewEditor{{RootID: "r", Path: "a.go", DisplayPath: "a.go", Unsaved: true, Selection: &ViewSelection{StartLine: 1, EndLine: 2}}},
		Diffs:    []ViewPath{{RootID: "r", Path: "b.go", DisplayPath: "b.go"}},
		Folder:   &ViewPath{RootID: "r", Path: "src", DisplayPath: "src"},
		Search:   &ViewSearch{Query: "Token", Hit: location("c.go")},
		Findings: []ViewFinding{{Fingerprint: "fp", RuleID: "rule", Level: FindingLevelHigh, Location: location("d.go")}, {Fingerprint: "fp2", RuleID: "rule", Level: FindingLevelLow}},
	}
	var fields []string
	mapped := view.MapText(func(field, value string) string {
		fields = append(fields, field)
		return "<" + value + ">"
	})
	want := []string{
		"source_context.view.editors[0].path", "source_context.view.editors[0].display_path",
		"source_context.view.diffs[0].path", "source_context.view.diffs[0].display_path",
		"source_context.view.folder.path", "source_context.view.folder.display_path",
		"source_context.view.search.query", "source_context.view.search.hit.path", "source_context.view.search.hit.display_path",
		"source_context.view.findings[0].location.path", "source_context.view.findings[0].location.display_path",
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("visited %v\nwant %v", fields, want)
	}
	if mapped.Editors[0].Path != "<a.go>" || mapped.Folder.DisplayPath != "<src>" || mapped.Search.Query != "<Token>" || mapped.Findings[0].Location.Path != "<d.go>" {
		t.Fatalf("mapped = %+v", mapped)
	}
	if mapped.Findings[0].RuleID != "rule" || mapped.Findings[0].Location.Line != 7 || mapped.Findings[1].Location != nil || !mapped.Editors[0].Unsaved {
		t.Fatalf("non-text facts changed: %+v", mapped.Findings)
	}
	if strings.HasPrefix(view.Editors[0].Path, "<") || strings.HasPrefix(view.Search.Hit.Path, "<") {
		t.Fatal("MapText changed the original view")
	}
}

func TestViewContextMapTextKeepsAnEmptyView(t *testing.T) {
	mapped := ViewContext{}.MapText(func(_, value string) string { return value })
	if mapped.Folder != nil || mapped.Search != nil || len(mapped.Editors)+len(mapped.Diffs)+len(mapped.Findings) != 0 {
		t.Fatalf("empty view mapped to %+v", mapped)
	}
}
