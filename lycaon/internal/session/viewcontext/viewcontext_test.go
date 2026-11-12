package viewcontext

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/viewstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeViews struct {
	reports []viewstate.Report
	person  string
}

func (f *fakeViews) Visible(_, personID string) []viewstate.Report {
	f.person = personID
	return f.reports
}

type fakeEditors struct {
	byDocument map[string]api.ViewEditor
	shown      []ShownEditor
}

func (f *fakeEditors) VisibleEditors(_ context.Context, _ string, _ sourcebranch.ID, _ string, shown []ShownEditor) ([]api.ViewEditor, error) {
	f.shown = shown
	var out []api.ViewEditor
	for _, s := range shown {
		if e, ok := f.byDocument[s.DocumentID]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

type fakeStore struct{ patched map[string]api.ViewContext }

func (s *fakeStore) PatchMessageViewContext(_ context.Context, _, messageID string, view api.ViewContext) (api.Message, error) {
	s.patched[messageID] = view
	return api.Message{ID: messageID, ViewContext: &view, Seq: int64(len(s.patched))}, nil
}

var (
	primary = "11111111-1111-1111-1111-111111111111"
	docs    = "22222222-2222-2222-2222-222222222222"
	outside = "33333333-3333-3333-3333-333333333333"
	scope   = Scope{Roots: []projectroot.RootRef{{ID: primary, IsPrimary: true}, {ID: docs, Label: "docs"}}}
)

func coordinator() *api.Session {
	return &api.Session{ID: "s", ProjectID: "p", AgentType: "coordinator"}
}

func prompt(id, author string, view *api.ViewContext) api.Message {
	return api.Message{ID: id, Role: api.MessageRoleUser, AuthorPersonID: author, ViewContext: view}
}

type fixture struct {
	m       *Service
	views   *fakeViews
	editors *fakeEditors
	store   *fakeStore
}

func newFixture(reports ...viewstate.Report) fixture {
	f := fixture{views: &fakeViews{reports: reports}, editors: &fakeEditors{byDocument: map[string]api.ViewEditor{}}, store: &fakeStore{patched: map[string]api.ViewContext{}}}
	f.m = New(f.store)
	f.m.SetSources(f.views, f.editors)
	return f
}

func report(client string, view api.ReplaceProjectViewRequest) viewstate.Report {
	view.ClientID = client
	return viewstate.Report{ClientID: client, View: view}
}

func noView() api.ViewContext {
	return api.ViewContext{Editors: []api.ViewEditor{}, Diffs: []api.ViewPath{}, Findings: []api.ViewFinding{}}
}

func TestRecordGathersEveryVisibleSurfaceInWindowOrder(t *testing.T) {
	selection := &api.ViewSelection{StartLine: 4, EndLine: 9}
	f := newFixture(
		report("window:a", api.ReplaceProjectViewRequest{
			Editor: &api.ViewReportEditor{DocumentID: "doc-guide"},
			Folder: &api.ViewReportPath{RootID: docs, Path: "api"},
			Search: &api.ViewReportSearch{Query: "LegacyToken", Hit: &api.ViewReportLocation{RootID: primary, Path: "auth.go", Line: 12}},
		}),
		report("window:b", api.ReplaceProjectViewRequest{
			Diff:     &api.ViewReportPath{RootID: primary, Path: "login.go"},
			Folder:   &api.ViewReportPath{RootID: primary, Path: "older"},
			Findings: []api.ViewReportFinding{{Fingerprint: "fp", RuleID: "go.sqli", Level: api.FindingLevelHigh, Location: &api.ViewReportLocation{RootID: primary, Path: "db.go", Line: 3}}},
		}),
	)
	f.editors.byDocument["doc-guide"] = api.ViewEditor{RootID: docs, Path: "guide.md", Unsaved: true, Selection: selection}

	got := f.m.Record(t.Context(), coordinator(), scope, "u1", []api.Message{prompt("u1", "alex", nil)})

	want := api.ViewContext{
		Editors:  []api.ViewEditor{{RootID: docs, Path: "guide.md", DisplayPath: "@docs/guide.md", Unsaved: true, Selection: selection}},
		Diffs:    []api.ViewPath{{RootID: primary, Path: "login.go", DisplayPath: "login.go"}},
		Folder:   &api.ViewPath{RootID: docs, Path: "api", DisplayPath: "@docs/api"},
		Search:   &api.ViewSearch{Query: "LegacyToken", Hit: &api.ViewLocation{RootID: primary, Path: "auth.go", DisplayPath: "auth.go", Line: 12}},
		Findings: []api.ViewFinding{{Fingerprint: "fp", RuleID: "go.sqli", Level: api.FindingLevelHigh, Location: &api.ViewLocation{RootID: primary, Path: "db.go", DisplayPath: "db.go", Line: 3}}},
	}
	if !reflect.DeepEqual(f.store.patched["u1"], want) || got[0].ViewContext == nil || !reflect.DeepEqual(*got[0].ViewContext, want) {
		t.Fatalf("recorded %+v\nwant     %+v", f.store.patched["u1"], want)
	}
	if f.views.person != "alex" || !reflect.DeepEqual(f.editors.shown, []ShownEditor{{ClientID: "window:a", DocumentID: "doc-guide"}}) {
		t.Fatalf("read the view of %q with editors %+v; want the sender's windows", f.views.person, f.editors.shown)
	}
}

func TestRecordLeavesOutWhatLiesOutsideTheSessionRoots(t *testing.T) {
	f := newFixture(report("window:a", api.ReplaceProjectViewRequest{
		Editor: &api.ViewReportEditor{DocumentID: "doc-out"},
		Diff:   &api.ViewReportPath{RootID: outside, Path: "x.go"},
		Folder: &api.ViewReportPath{RootID: outside, Path: "x"},
		Search: &api.ViewReportSearch{Query: "q", Hit: &api.ViewReportLocation{RootID: outside, Path: "x.go"}},
	}))
	f.editors.byDocument["doc-out"] = api.ViewEditor{RootID: outside, Path: "x.go"}
	f.m.Record(t.Context(), coordinator(), scope, "u1", []api.Message{prompt("u1", "alex", nil)})
	want := noView()
	want.Search = &api.ViewSearch{Query: "q"}
	if got := f.store.patched["u1"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded %+v; want only the query", got)
	}
}

func TestRecordStoresOnlyChangedViews(t *testing.T) {
	folder := viewstate.Report{ClientID: "w", View: api.ReplaceProjectViewRequest{ClientID: "w", Folder: &api.ViewReportPath{RootID: primary, Path: "a"}}}
	shown := noView()
	shown.Folder = &api.ViewPath{RootID: primary, Path: "a", DisplayPath: "a"}
	nothing := noView()
	cases := []struct {
		name     string
		previous *api.ViewContext
		reports  []viewstate.Report
		want     *api.ViewContext
	}{
		{"nothing in view, nothing seen", nil, nil, nil},
		{"unchanged", &shown, []viewstate.Report{folder}, nil},
		{"cleared after a delivered view", &shown, nil, &nothing},
		{"still clear", &nothing, nil, nil},
		{"shown after clearing", &nothing, []viewstate.Report{folder}, &shown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(tc.reports...)
			history := []api.Message{prompt("u1", "alex", tc.previous), {ID: "a1", Role: api.MessageRoleAssistant}, prompt("u2", "alex", nil)}
			f.m.Record(t.Context(), coordinator(), scope, "u2", history)
			got, recorded := f.store.patched["u2"]
			if tc.want == nil && recorded || tc.want != nil && (!recorded || !reflect.DeepEqual(got, *tc.want)) {
				t.Fatalf("recorded %v %+v; want %+v", recorded, got, tc.want)
			}
		})
	}
}

func TestRecordBoundsEachList(t *testing.T) {
	var findings []api.ViewReportFinding
	for i := range MaxItems + 3 {
		findings = append(findings, api.ViewReportFinding{Fingerprint: fmt.Sprint(i), RuleID: "r", Level: api.FindingLevelInfo})
	}
	f := newFixture(report("w", api.ReplaceProjectViewRequest{Findings: findings}))
	f.m.Record(t.Context(), coordinator(), scope, "u1", []api.Message{prompt("u1", "alex", nil)})
	if got := f.store.patched["u1"].Findings; len(got) != MaxItems || got[0].Fingerprint != "0" {
		t.Fatalf("findings = %d; want the first %d", len(got), MaxItems)
	}
}

func TestRecordSkipsTurnsWithoutASenderView(t *testing.T) {
	worker := coordinator()
	worker.ParentSessionID = "parent"
	folder := report("w", api.ReplaceProjectViewRequest{Folder: &api.ViewReportPath{RootID: primary, Path: "a"}})
	cases := []struct {
		name    string
		sess    *api.Session
		opening string
		history []api.Message
	}{
		{"worker child", worker, "u1", []api.Message{prompt("u1", "alex", nil)}},
		{"not a coordinator", &api.Session{ID: "s", AgentType: "explore"}, "u1", []api.Message{prompt("u1", "alex", nil)}},
		{"continuation", coordinator(), "", []api.Message{prompt("u1", "alex", nil)}},
		{"host-authored prompt", coordinator(), "u1", []api.Message{prompt("u1", "", nil)}},
		{"already recorded", coordinator(), "u1", []api.Message{prompt("u1", "alex", &api.ViewContext{})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(folder)
			f.m.Record(t.Context(), tc.sess, scope, tc.opening, tc.history)
			if len(f.store.patched) != 0 {
				t.Fatalf("recorded %+v", f.store.patched)
			}
		})
	}
}
