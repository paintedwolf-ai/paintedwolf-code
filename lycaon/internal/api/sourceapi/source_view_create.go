package sourceapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// maxSourceViewCreateAttempts bounds re-reads while project folders keep changing.
const maxSourceViewCreateAttempts = 3

func (s *Views) HandleCreateSourceView(w http.ResponseWriter, r *http.Request) {
	// The generation precedes the project read, so a view built from folders
	// that change before it is registered is rebuilt rather than retained.
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	generation := s.sourceViewRegistry().projectGeneration(projectID)
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	var request wire.SourceViewCreate
	if err := httpio.DecodeSourceJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	for attempt := 1; ; attempt++ {
		if s.createSourceView(w, r, p, request, generation, attempt == maxSourceViewCreateAttempts) {
			return
		}
		generation = s.sourceViewRegistry().projectGeneration(projectID)
		if p, ok = requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r); !ok {
			return
		}
	}
}

// createSourceView answers the request, or reports false when the project's
// folders changed while the view was built and the caller should retry.
func (s *Views) createSourceView(w http.ResponseWriter, r *http.Request, p *project.Project, request wire.SourceViewCreate, generation uint64, final bool) bool {
	if err := validateSourceViewCreate(p, request); err != nil {
		s.writeSourceViewError(w, r, err)
		return true
	}
	operationID, clientID, sessionID := sourceViewCreateIdentity(request)
	scoped, err := requestscope.ResolveSessionProject(s.SessionStore, r.Context(), p, sessionID)
	if err != nil {
		s.writeSourceViewError(w, r, err)
		return true
	}
	p = scoped.Project
	if request.Tree != nil && request.Tree.WorkspaceID != p.WorkspaceID() {
		s.Workspace.writeSourceWorkspaceMismatch(w, request.Tree.WorkspaceID, p.WorkspaceID())
		return true
	}
	canonical, err := sourceViewCanonical(request)
	if err != nil {
		s.writeSourceViewError(w, r, err)
		return true
	}
	scope := pagedview.Scope{Person: requestscope.Caller(r).ID, Project: p.ID}
	key := sourceViewCreateKey{scope: scope, client: clientID, operation: operationID}
	//nolint:contextcheck // Accepted views follow server shutdown and explicit release, not request cancellation.
	view, release, created, err := s.sourceViewRegistry().create(r.Context(), key, canonical, generation, func() *sourceView {
		if request.Tree != nil {
			return s.Trees.newTreeView(scope, p, *request.Tree)
		}
		return s.ComparisonViews.newComparisonView(scope, p, *request.Comparison)
	})
	if errors.Is(err, errSourceViewProjectChanged) && !final {
		return false
	}
	if err != nil {
		s.writeSourceViewError(w, r, err)
		return true
	}
	// The preparation pin protects accepted-state construction.
	if _, err := s.sourceViewProject(r.Context(), p, view); err != nil {
		if created {
			view.cancel()
			s.sourceViewRegistry().registry.Release(scope, view.id)
		}
		release()
		s.writeSourceViewError(w, r, err)
		return true
	}
	snapshot, snapshotErr := view.snapshot(r.Context())
	if created {
		if view.navigation.tree != nil {
			s.Trees.prepareTreeView(view, release)
		} else {
			s.ComparisonViews.prepareComparisonView(view, p, release)
		}
	} else {
		release()
	}
	if snapshotErr != nil {
		s.writeSourceViewError(w, r, snapshotErr)
		return true
	}
	w.Header().Set("Location", SourceViewURL(p.ID, view.id))
	httpio.WriteJSON(w, http.StatusCreated, snapshot)
	return true
}

func SourceViewURL(projectID, viewID string) string {
	return "/v1/projects/" + projectID + "/source/views/" + viewID
}

func sourceViewCreateIdentity(request wire.SourceViewCreate) (string, string, string) {
	if request.Tree != nil {
		return request.Tree.OperationID, request.Tree.ClientID, request.Tree.SessionID
	}
	return request.Comparison.OperationID, request.Comparison.ClientID, request.Comparison.SessionID
}

func rejectedSourceIntent(message string) error {
	return &comparisonFailure{wire.ApiErrorCodeInvalidRequest, message}
}

func validateSourceViewCreate(p *project.Project, request wire.SourceViewCreate) error {
	if err := request.Validate(); err != nil {
		return rejectedSourceIntent(err.Error())
	}
	id, client, session := sourceViewCreateIdentity(request)
	if _, err := uuid.Parse(id); err != nil {
		return rejectedSourceIntent("operation_id must be a UUID.")
	}
	if strings.TrimSpace(client) == "" || len(client) > 256 {
		return rejectedSourceIntent("client_id must contain between 1 and 256 bytes.")
	}
	if session != "" {
		if _, err := uuid.Parse(session); err != nil {
			return rejectedSourceIntent("session_id must be a UUID.")
		}
	}
	if request.Tree != nil {
		if request.Tree.WorkspaceID == "" {
			return rejectedSourceIntent("workspace_id is required.")
		}
		if len(request.Tree.Intent.Disclosures) > 2048 || len(request.Tree.Intent.Filter) > 4096 {
			return pagedview.ErrBudget
		}
		for _, disclosure := range request.Tree.Intent.Disclosures {

			if err := validateSourceTreeAddress(p, disclosure.Address); err != nil {
				return err
			}
		}
		return validateTreeReview(request.Tree.Intent.Review)
	}
	if err := request.Comparison.Source.Validate(); err != nil {
		return rejectedSourceIntent(err.Error())
	}
	if source := request.Comparison.Source.Current; source != nil {
		if err := validateSourceTreeAddress(p, wire.SourceTreeAddress{RootID: source.RootID, Path: source.Path}); err != nil {
			return err
		}
		if source.DecodeAs != "" && source.DecodeAs != "utf-16le" && source.DecodeAs != "utf-16be" {
			return rejectedSourceIntent("Unsupported source encoding.")
		}
	}
	return validateSourceComparisonIntent(request.Comparison.Intent)
}

func validateSourceTreeAddress(p *project.Project, address wire.SourceTreeAddress) error {
	if address.Path == "" || len(address.Path) > 65536 || strings.ContainsAny(address.Path, "\\\x00") || strings.HasPrefix(address.Path, "/") {
		return pagedview.ErrRange
	}
	for _, part := range strings.Split(address.Path, "/") {
		if part == ".." || part == "" || part == "." && address.Path != "." {
			return pagedview.ErrRange
		}
	}
	for _, root := range p.Roots {
		if root.ID == address.RootID {
			return nil
		}
	}
	return sourcetree.ErrUnknownRoot
}

func validateSourceComparisonIntent(intent wire.SourceComparisonIntent) error {
	switch intent.Mode {
	case "full", "changes", "split", "before", "after":
	default:
		return rejectedSourceIntent("Unknown comparison display mode.")
	}
	if len(intent.Expanded) > 2048 {
		return pagedview.ErrBudget
	}
	for _, fold := range intent.Expanded {
		if fold.Start < 0 || fold.End <= fold.Start {
			return pagedview.ErrRange
		}
	}
	return nil
}
