// Package viewcontext records what a prompt's sender had in view.
package viewcontext

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/viewstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// MaxItems bounds each list a view context carries.
const MaxItems = 8

// Views lists a person's live window reports, most recently focused first.
type Views interface {
	Visible(projectID, personID string) []viewstate.Report
}

// ShownEditor is a document a window's visible editor shows.
type ShownEditor struct {
	ClientID, DocumentID string
}

// Editors resolves shown documents of a project branch, in order, with each
// window's own selection; documents outside the branch are left out.
type Editors interface {
	VisibleEditors(ctx context.Context, projectID string, branch sourcebranch.ID, personID string, shown []ShownEditor) ([]api.ViewEditor, error)
}

type Store interface {
	PatchMessageViewContext(ctx context.Context, sessionID, messageID string, view api.ViewContext) (api.Message, error)
}

// Scope is where the session works: its roots, session folder, and project branch.
type Scope struct {
	Roots        []projectroot.RootRef
	ActiveRootID string
	Branch       sourcebranch.ID
}

type Service struct {
	store   Store
	views   Views
	editors Editors
}

func New(store Store) *Service { return &Service{store: store} }

func (m *Service) SetSources(views Views, editors Editors) { m.views, m.editors = views, editors }

// Record stores the sender's view on the prompt that opened a coordinator turn
// when it differs from the latest view still in history, and returns history
// carrying it.
func (m *Service) Record(ctx context.Context, sess *api.Session, scope Scope, openingMessageID string, history []api.Message) []api.Message {
	if m == nil || m.views == nil || m.editors == nil || sess == nil || !surface.IsCoordinatorSession(sess) || sess.IsWorkerChild() {
		return history
	}
	opening := promptIndex(history, openingMessageID)
	if opening < 0 || viewOf(history[opening]) != nil {
		return history
	}
	person := strings.TrimSpace(history[opening].AuthorPersonID)
	if person == "" {
		return history
	}
	current, err := m.build(ctx, sess.ProjectID, person, scope)
	if err != nil {
		slog.WarnContext(ctx, "view context not read", "session_id", sess.ID, "err", err)
		return history
	}
	previous := latestView(history[:opening])
	if previous == nil && empty(current) || previous != nil && reflect.DeepEqual(*previous, current) {
		return history
	}
	stored, err := m.store.PatchMessageViewContext(ctx, sess.ID, openingMessageID, current)
	if err != nil {
		slog.WarnContext(ctx, "view context not recorded", "session_id", sess.ID, "err", err)
		return history
	}
	out := append([]api.Message(nil), history...)
	out[opening].SourceContext, out[opening].Seq = stored.SourceContext, stored.Seq
	return out
}

func (m *Service) build(ctx context.Context, projectID, person string, scope Scope) (api.ViewContext, error) {
	in := func(rootID string) bool {
		return slices.ContainsFunc(scope.Roots, func(root projectroot.RootRef) bool { return root.ID == rootID })
	}
	pathOf := func(p api.ViewReportPath) api.ViewPath {
		return api.ViewPath{RootID: p.RootID, Path: p.Path, DisplayPath: projectroot.DisplayPath(scope.Roots, scope.ActiveRootID, p.RootID, p.Path)}
	}
	locationOf := func(l *api.ViewReportLocation) *api.ViewLocation {
		if l == nil || !in(l.RootID) {
			return nil
		}
		return &api.ViewLocation{RootID: l.RootID, Path: l.Path, DisplayPath: projectroot.DisplayPath(scope.Roots, scope.ActiveRootID, l.RootID, l.Path), Line: l.Line}
	}
	out := api.ViewContext{Editors: []api.ViewEditor{}, Diffs: []api.ViewPath{}, Findings: []api.ViewFinding{}}
	var shown []ShownEditor
	for _, report := range m.views.Visible(projectID, person) {
		v := report.View
		if v.Editor != nil {
			shown = append(shown, ShownEditor{ClientID: report.ClientID, DocumentID: v.Editor.DocumentID})
		}
		if v.Diff != nil && in(v.Diff.RootID) && len(out.Diffs) < MaxItems {
			if diff := pathOf(*v.Diff); !slices.Contains(out.Diffs, diff) {
				out.Diffs = append(out.Diffs, diff)
			}
		}
		if v.Folder != nil && out.Folder == nil && in(v.Folder.RootID) {
			folder := pathOf(*v.Folder)
			out.Folder = &folder
		}
		if v.Search != nil && out.Search == nil {
			out.Search = &api.ViewSearch{Query: v.Search.Query, Hit: locationOf(v.Search.Hit)}
		}
		for _, f := range v.Findings {
			seen := slices.ContainsFunc(out.Findings, func(g api.ViewFinding) bool { return g.Fingerprint == f.Fingerprint })
			if !seen && len(out.Findings) < MaxItems {
				out.Findings = append(out.Findings, api.ViewFinding{Fingerprint: f.Fingerprint, RuleID: f.RuleID, Level: f.Level, Location: locationOf(f.Location)})
			}
		}
	}
	editors, err := m.editors.VisibleEditors(ctx, projectID, scope.Branch, person, shown)
	if err != nil {
		return api.ViewContext{}, err
	}
	for _, e := range editors {
		if !in(e.RootID) || len(out.Editors) == MaxItems {
			continue
		}
		e.DisplayPath = projectroot.DisplayPath(scope.Roots, scope.ActiveRootID, e.RootID, e.Path)
		if !slices.ContainsFunc(out.Editors, func(f api.ViewEditor) bool { return f.RootID == e.RootID && f.Path == e.Path }) {
			out.Editors = append(out.Editors, e)
		}
	}
	return out, nil
}

func viewOf(m api.Message) *api.ViewContext {
	if m.SourceContext == nil {
		return nil
	}
	return m.SourceContext.View
}

func empty(v api.ViewContext) bool {
	return len(v.Editors) == 0 && len(v.Diffs) == 0 && v.Folder == nil && v.Search == nil && len(v.Findings) == 0
}

func promptIndex(history []api.Message, id string) int {
	if strings.TrimSpace(id) == "" {
		return -1
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].ID == id && history[i].Role == api.MessageRoleUser {
			return i
		}
	}
	return -1
}

func latestView(history []api.Message) *api.ViewContext {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == api.MessageRoleUser && viewOf(history[i]) != nil {
			return viewOf(history[i])
		}
	}
	return nil
}
