package survey

import (
	"context"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/summarize"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Access is bound once from host facts; gathering services cannot replace its authority.
type summaryAccess struct {
	reads         *projectpaths.ReadSession
	boundary      *sandbox.Boundary
	catalog       sourceInventoryCatalog
	trees         *sourcecatalog.TreeStores
	literalsIndex *sourcecatalog.LiteralSearch
	projectID     string
	profileID     string
	activeRoot    string
	primary       projectroot.RootRef
	primaryValid  bool
	drafts        func(context.Context) sourceview.DraftOverlay
	literals      func(context.Context, string, []string, sandbox.SurveyOptions, func(grepMatch)) error
}

func newSummaryAccess(boundary *sandbox.Boundary, reads *projectpaths.ReadSession, tctx tools.ToolContext, catalog *sourcecatalog.Catalog) *summaryAccess {
	// The call's root set remains stable while observers update their own projections.
	tctx.Source.Roots = append([]projectroot.RootRef(nil), tctx.Source.Roots...)
	primary, err := projectroot.PrimaryRoot(tctx.Source.Roots)
	access := &summaryAccess{
		reads: reads, boundary: boundary, catalog: catalogOrProcess(catalog), trees: catalogOrProcess(catalog).Trees, literalsIndex: catalogOrProcess(catalog).Literals,
		projectID: tctx.Identity.ProjectID, profileID: tctx.ProfileID(), activeRoot: tctx.ActiveRootPath(),
		primary: primary, primaryValid: err == nil,
		drafts: func(ctx context.Context) sourceview.DraftOverlay { return sourceview.DraftsFor(ctx, tctx) },
	}
	access.literals = func(ctx context.Context, path string, terms []string, policy sandbox.SurveyOptions, visit func(grepMatch)) error {
		return scanLiteralMatches(ctx, boundary, catalog, tctx, path, terms, policy, visit)
	}
	return access
}

func (a *summaryAccess) qualify(root projectroot.RootRef, abs string) string {
	if !a.primaryValid {
		return abs
	}
	return projectroot.Qualify(a.primary, root, abs)
}

// Only a complete, exact source window can establish the same document basis
// as read. Internal gathering and partial summaries cannot advance it.
func rememberSummaryReads(ctx context.Context, reads *projectpaths.ReadSession, tctx tools.ToolContext, windows []summarize.PackWindow) {
	for _, window := range windows {
		if window.StartLine != 1 || window.Body == "" || window.Path == "inline" {
			continue
		}
		resolved, err := reads.Resolve(ctx, window.Path)
		if err != nil {
			continue
		}
		documents, ok := sourceview.DocumentsFor(tctx, resolved)
		if !ok {
			continue
		}
		branch, err := tctx.SourceBranch(resolved.Root.ID)
		if err != nil {
			continue
		}
		doc, open, err := documents.OpenDocument(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, resolved.ScopeRel)
		if err != nil || !open {
			continue
		}
		lines := strings.Split(doc.Text, "\n")
		if window.EndLine != len(lines) || window.Body != hostmarker.FormatNumberedLines(lines, 1) {
			continue
		}
		documents.RememberAgentRead(tctx.Identity.ProjectID, tctx.Identity.SessionID, doc)
	}
}
