package survey

import (
	"context"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Access is bound once from host facts; gathering services cannot replace its authority.
type summaryAccess struct {
	reads        *projectpaths.ReadSession
	boundary     *sandbox.Boundary
	catalog      *sourcecatalog.Catalog
	projectID    string
	profileID    string
	activeRoot   string
	primary      projectroot.RootRef
	primaryValid bool
	drafts       func(context.Context) sourceview.DraftOverlay
	literals     func(context.Context, string, []string, sandbox.SurveyOptions, func(grepMatch)) error
}

func newSummaryAccess(boundary *sandbox.Boundary, reads *projectpaths.ReadSession, tctx tools.ToolContext, catalog *sourcecatalog.Catalog) *summaryAccess {
	// The call's root set remains stable while observers update their own projections.
	tctx.Roots = append([]projectroot.RootRef(nil), tctx.Roots...)
	primary, err := projectroot.PrimaryRoot(tctx.Roots)
	access := &summaryAccess{
		reads: reads, boundary: boundary, catalog: catalog,
		projectID: tctx.ProjectID, profileID: tctx.ProfileID(), activeRoot: tctx.ActiveRootPath(),
		primary: primary, primaryValid: err == nil,
		drafts: func(ctx context.Context) sourceview.DraftOverlay { return sourceview.DraftsFor(ctx, tctx) },
	}
	access.literals = func(ctx context.Context, path string, terms []string, policy sandbox.SurveyOptions, visit func(grepMatch)) error {
		return scanLiteralMatches(ctx, boundary, access.catalog, tctx, path, terms, policy, visit)
	}
	return access
}

func (a *summaryAccess) qualify(root projectroot.RootRef, abs string) string {
	if !a.primaryValid {
		return abs
	}
	return projectroot.Qualify(a.primary, root, abs)
}
