package inject_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/pkg/api"
)

func view() api.ViewContext {
	return api.ViewContext{Editors: []api.ViewEditor{}, Diffs: []api.ViewPath{}, Findings: []api.ViewFinding{}}
}

func render(t *testing.T, v api.ViewContext, readOffered bool) string {
	t.Helper()
	return inject.RenderViewContextBlock(context.Background(), promptstest.InjectRenderer(t), "sess", v, readOffered)
}

func lineWith(block, value string) string {
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, value) {
			return line
		}
	}
	return ""
}

// Each state's row carries the values the state supplies, and no other row borrows them.
func TestRenderViewContextBlockStatesEverySuppliedFact(t *testing.T) {
	cases := []struct {
		name string
		set  func(*api.ViewContext)
		key  string
		want []string
	}{
		{"editor", func(v *api.ViewContext) {
			v.Editors = []api.ViewEditor{{DisplayPath: "src/app.ts"}}
		}, "src/app.ts", nil},
		{"selection", func(v *api.ViewContext) {
			v.Editors = []api.ViewEditor{{DisplayPath: "@docs/guide.md", Selection: &api.ViewSelection{StartLine: 42, EndLine: 88}}}
		}, "@docs/guide.md", []string{"42", "88"}},
		{"one line", func(v *api.ViewContext) {
			v.Editors = []api.ViewEditor{{DisplayPath: "a.go", Selection: &api.ViewSelection{StartLine: 7, EndLine: 7}}}
		}, "a.go", []string{"7"}},
		{"cursor", func(v *api.ViewContext) {
			v.Editors = []api.ViewEditor{{DisplayPath: "b.go", Selection: &api.ViewSelection{StartLine: 13, EndLine: 13, Cursor: true}}}
		}, "b.go", []string{"13"}},
		{"diff", func(v *api.ViewContext) { v.Diffs = []api.ViewPath{{DisplayPath: "login.go"}} }, "login.go", nil},
		{"folder", func(v *api.ViewContext) { v.Folder = &api.ViewPath{DisplayPath: "@docs/api"} }, "@docs/api", nil},
		{"search", func(v *api.ViewContext) {
			v.Search = &api.ViewSearch{Query: `Legacy "Token"`, Hit: &api.ViewLocation{DisplayPath: "auth.go", Line: 31}}
		}, strconv.Quote(`Legacy "Token"`), []string{"auth.go:31"}},
		{"finding", func(v *api.ViewContext) {
			v.Findings = []api.ViewFinding{{RuleID: "go.sqli", Level: api.FindingLevelHigh, Location: &api.ViewLocation{DisplayPath: "db.go", Line: 9}}}
		}, "go.sqli", []string{string(api.FindingLevelHigh), "db.go:9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := view()
			tc.set(&v)
			got := render(t, v, true)
			row := lineWith(got, tc.key)
			for _, want := range append([]string{tc.key}, tc.want...) {
				if !strings.Contains(row, want) {
					t.Fatalf("row %q lost the supplied value %q:\n%s", row, want, got)
				}
			}
		})
	}
}

func TestRenderViewContextBlockMarksUnsavedEditors(t *testing.T) {
	saved, unsaved := view(), view()
	saved.Editors = []api.ViewEditor{{DisplayPath: "a.go"}}
	unsaved.Editors = []api.ViewEditor{{DisplayPath: "a.go", Unsaved: true}}
	if lineWith(render(t, saved, true), "a.go") == lineWith(render(t, unsaved, true), "a.go") {
		t.Fatal("unsaved state did not change the editor row")
	}
}

func TestRenderViewContextBlockStatesAnEmptyView(t *testing.T) {
	shown := view()
	shown.Folder = &api.ViewPath{DisplayPath: "src"}
	empty, withFolder := render(t, view(), true), render(t, shown, true)
	if empty == "" || empty == withFolder || strings.Contains(empty, "src") {
		t.Fatalf("empty view block = %q", empty)
	}
}

func TestRenderViewContextBlockOffersReadOnlyForAnOfferedEditor(t *testing.T) {
	editor, folder := view(), view()
	editor.Editors = []api.ViewEditor{{DisplayPath: "a.go"}}
	folder.Folder = &api.ViewPath{DisplayPath: "src"}
	if !strings.Contains(render(t, editor, true), "`read`") || strings.Contains(render(t, editor, false), "`read`") || strings.Contains(render(t, folder, true), "`read`") {
		t.Fatal("read is offered only beside an editor on a surface that has it")
	}
}
