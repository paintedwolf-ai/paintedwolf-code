package editoradmin

import (
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestInvalidProjectViewNamesTheFieldAtFault(t *testing.T) {
	const root, doc = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	valid := func() wire.ReplaceProjectViewRequest {
		return wire.ReplaceProjectViewRequest{
			ClientID: "window:main",
			Editor:   &wire.ViewReportEditor{DocumentID: doc},
			Diff:     &wire.ViewReportPath{RootID: root, Path: "src/a.go"},
			Folder:   &wire.ViewReportPath{RootID: root, Path: "src"},
			Search:   &wire.ViewReportSearch{Query: "Token", Hit: &wire.ViewReportLocation{RootID: root, Path: "src/a.go", Line: 3}},
			Findings: []wire.ViewReportFinding{{Fingerprint: "fp", RuleID: "rule", Level: wire.FindingLevelHigh, Location: &wire.ViewReportLocation{RootID: root, Path: "src/a.go", Line: 9}}},
		}
	}
	if field, reason := invalidProjectView(valid()); field != "" {
		t.Fatalf("valid report rejected at %s: %s", field, reason)
	}
	cases := map[string]func(*wire.ReplaceProjectViewRequest){
		"client_id":          func(r *wire.ReplaceProjectViewRequest) { r.ClientID = " " },
		"editor.document_id": func(r *wire.ReplaceProjectViewRequest) { r.Editor.DocumentID = "doc" },
		"diff":               func(r *wire.ReplaceProjectViewRequest) { r.Diff.Path = "../outside" },
		"folder":             func(r *wire.ReplaceProjectViewRequest) { r.Folder.Path = "/abs" },
		"search.query":       func(r *wire.ReplaceProjectViewRequest) { r.Search.Query = strings.Repeat("q", 1025) },
		"search.hit":         func(r *wire.ReplaceProjectViewRequest) { r.Search.Hit.RootID = "root" },
		"findings":           func(r *wire.ReplaceProjectViewRequest) { r.Findings[0].Level = "severe" },
	}
	for want, breakIt := range cases {
		t.Run(want, func(t *testing.T) {
			req := valid()
			breakIt(&req)
			if field, _ := invalidProjectView(req); field != want {
				t.Fatalf("rejected at %q; want %q", field, want)
			}
		})
	}
	tooMany := valid()
	for len(tooMany.Findings) <= 12 {
		tooMany.Findings = append(tooMany.Findings, tooMany.Findings[0])
	}
	if field, _ := invalidProjectView(tooMany); field != "findings" {
		t.Fatalf("thirteen findings rejected at %q", field)
	}
}
