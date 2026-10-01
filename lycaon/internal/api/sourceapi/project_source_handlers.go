package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceViewerWorkspace struct {
	id     string
	kind   wire.SourceWorkspaceKind
	branch sourcebranch.ID
}

func (s *Handler) HandleGetProjectSource(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	req, includeDeleted, err := sourceViewerRequest(r)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	workerID := strings.TrimSpace(r.URL.Query().Get("worker_id"))
	branchRoot, workspaceID, workspaceKind, branch, releaseBranch := s.sourceReadWorkspace(r.Context(), p, workerID)
	defer releaseBranch()
	p, err = SourceProjectInBranch(p, branchRoot)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	if !branch.IsWorker() {
		rootID, _, rootErr := project.ResolveWorkspaceRoot(p, req.RootID)
		if rootErr != nil {
			s.writeSourceReadError(w, r, rootErr)
			return
		}
		branch = p.BranchForRoot(rootID)
	}
	scope := sourceViewerWorkspace{id: workspaceID, kind: workspaceKind, branch: branch}
	observation, err := project.ObserveProjectSource(p, req)
	if errors.Is(err, project.ErrSourceNotFound) && includeDeleted && (workerID == "" || branch.IsWorker()) {
		var deleted *wire.ProjectSourceReadResponse
		deleted, err = s.readDeletedSource(r.Context(), p, req, scope)
		if err == nil {
			httpio.WriteJSON(w, http.StatusOK, deleted)
			return
		}
		// A replacement that arrived during history lookup takes the current read path.
		if errors.Is(err, project.ErrSourceExists) {
			observation, err = project.ObserveProjectSource(p, req)
		}
	}
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	result, err := s.projectSourceObservation(r.Context(), p.ID, observation, scope)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func sourceViewerRequest(r *http.Request) (project.SourceReadRequest, bool, error) {
	req := project.SourceReadRequest{
		Path:     strings.TrimSpace(r.URL.Query().Get("path")),
		RootID:   strings.TrimSpace(r.URL.Query().Get("root_id")),
		DecodeAs: strings.TrimSpace(r.URL.Query().Get("decode_as")),
	}
	includeDeleted, _, err := httpio.OptionalBoolQuery(r, "include_deleted")
	if err != nil {
		return req, false, err
	}
	if includeDeleted && req.RootID == "" {
		return req, false, fmt.Errorf("include_deleted requires root_id")
	}
	return req, includeDeleted, nil
}

// projectSourceObservation answers the read with the ledger's recorded
// identity for it; a read records nothing.
func (s *Handler) projectSourceObservation(ctx context.Context, projectID string, observation *project.SourceReadObservation, scope sourceViewerWorkspace) (wire.ProjectSourceReadResponse, error) {
	recorded, err := s.recordedSourceObservation(ctx, projectID, observation, scope)
	if err != nil {
		return wire.ProjectSourceReadResponse{}, err
	}
	result, err := observation.Project()
	if err != nil {
		return wire.ProjectSourceReadResponse{}, err
	}
	result.FileID, result.VersionID = recorded.FileID, recorded.VersionID
	return ToProjectSourceReadDTO(result, scope.id, scope.kind), nil
}

func (s *Handler) recordedSourceObservation(ctx context.Context, projectID string, observation *project.SourceReadObservation, scope sourceViewerWorkspace) (sourceledger.TrackedFile, error) {
	return s.SourceLedger.LookupFile(ctx, sourceledger.TrackInput{
		ProjectID: projectID, BranchID: scope.branch,
		RootID: observation.RootID, Path: observation.Path, EntryKind: sourceledger.EntryKindFile,
		SHA256: observation.Revision.SHA256, Content: observation.Revision.Bytes, Size: observation.SizeBytes,
	})
}

func (s *Handler) HandleGetSourceWorkspace(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	scoped, ok := requestscope.SessionProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	p = scoped.Project
	inventory, err := s.sourceInventoryState(r.Context(), p)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, SourceWorkspaceDTO(p, scoped.Binding != nil, mapSourceInventoryState(inventory, p.RootsGeneration)))
}

// SourceWorkspaceDTO sets sessionScoped when a chat's worktree, rather than the
// project's own checkout, produced these roots.
func SourceWorkspaceDTO(p *project.Project, sessionScoped bool, inventory wire.SourceInventoryState) wire.SourceWorkspace {
	roots := make([]wire.SourceWorkspaceRoot, 0, len(p.Roots))
	for _, root := range p.Roots {
		roots = append(roots, wire.SourceWorkspaceRoot{
			BranchID: string(p.BranchForRoot(root.ID)),
			ID:       root.ID, Path: root.Path, Watch: SourceWatchCoverageDTO(repochange.Coverage(root.Path)),
		})
	}
	return wire.SourceWorkspace{WorkspaceID: p.WorkspaceID(), SessionScoped: sessionScoped, Roots: roots, Inventory: inventory}
}

func SourceWatchCoverageDTO(coverage repochange.WatchCoverage) wire.SourceWatchCoverage {
	out := wire.SourceWatchCoverage{
		State: wire.SourceWatchLive, Recursive: coverage.Recursive, UnwatchedDirectories: coverage.Truncated,
	}
	switch {
	case !coverage.Watching:
		out.State = wire.SourceWatchUnwatched
	case coverage.Faulted:
		out.State = wire.SourceWatchFaulted
	case coverage.Truncated > 0:
		out.State = wire.SourceWatchPartial
	}
	return out
}

// sourceReadWorkspace resolves a read against a worker overlay. The branch is
// leased until release is called, so it stays on disk for the read.
func (s *Handler) sourceReadWorkspace(
	ctx context.Context,
	p *project.Project,
	workerID string,
) (branchRoot, workspaceID string, kind wire.SourceWorkspaceKind, branch sourcebranch.ID, release func()) {
	workspaceID, kind, branch = p.WorkspaceID(), wire.SourceWorkspaceKindProject, p.SourceBranch
	branchRoot, release = s.WorkerBranchRoot(ctx, p.ID, workerID)
	if branchRoot == "" {
		return "", workspaceID, kind, branch, release
	}
	task, _ := s.Workers.Get(workerID)
	roots := worker.TaskRootRefs(ctx, task, s.ProjectRegistry)
	workerBranch, branchErr := sourcebranch.ForWorker(workerID)
	if workerWorkspace := sourceworkspace.ID(p.ID, roots); workerWorkspace != "" && branchErr == nil {
		workspaceID, kind, branch = workerWorkspace, wire.SourceWorkspaceKindWorker, workerBranch
	}
	return branchRoot, workspaceID, kind, branch, release
}

func (s *Handler) writeSourceWorkspaceMismatch(
	w http.ResponseWriter,
	expectedWorkspaceID string,
	actualWorkspaceID string,
) {
	s.responses.FailDetails(w, wire.ApiErrorCodeSourceWorkspaceMismatch,
		map[string]any{
			"expected_workspace_id": expectedWorkspaceID,
			"actual_workspace_id":   actualWorkspaceID,
		},
		"the source belongs to another workspace",
	)
}

func (s *Handler) currentSourceWorkspaceID(
	w http.ResponseWriter,
	r *http.Request,
	workerID string,
) (string, bool) {
	current, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return "", false
	}
	current, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, current)
	if !ok {
		return "", false
	}
	_, actualWorkspaceID, _, _, releaseBranch := s.sourceReadWorkspace(r.Context(), current, workerID)
	releaseBranch()
	return actualWorkspaceID, true
}

func (s *Handler) sourceWorkspaceStillCurrent(
	w http.ResponseWriter,
	r *http.Request,
	expectedWorkspaceID string,
	workerID string,
) bool {
	actualWorkspaceID, ok := s.currentSourceWorkspaceID(w, r, workerID)
	if !ok {
		return false
	}
	if actualWorkspaceID == expectedWorkspaceID {
		return true
	}
	s.writeSourceWorkspaceMismatch(w, expectedWorkspaceID, actualWorkspaceID)
	return false
}

func (s *Handler) HandleGetProjectSourceRaw(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	workerID := strings.TrimSpace(q.Get("worker_id"))
	branchRoot, _, _, _, releaseBranch := s.sourceReadWorkspace(r.Context(), p, workerID)
	defer releaseBranch()
	p, err := SourceProjectInBranch(p, branchRoot)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	result, err := project.ReadProjectSourceRaw(p, project.SourceReadRequest{
		Path:   strings.TrimSpace(q.Get("path")),
		RootID: strings.TrimSpace(q.Get("root_id")),
	})
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Bytes)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- sniffed image bytes; sandbox CSP disables active SVG content.
	_, _ = w.Write(result.Bytes)
}

// A usable worker overlay stays leased until release; unavailable trees return no root.
func (s *Handler) WorkerBranchRoot(ctx context.Context, projectID, rawWorkerID string) (string, func()) {
	noop := func() {}
	workerID := strings.TrimSpace(rawWorkerID)
	if workerID == "" {
		return "", noop
	}
	task, ok := s.Workers.Get(workerID)
	if !ok || task == nil {
		return "", noop
	}
	if strings.TrimSpace(task.ProjectID) != strings.TrimSpace(projectID) || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return "", noop
	}
	_, lease, err := s.Workers.EnsureWorkerBranch(ctx, workerID)
	if err != nil {
		s.responses.Logger.DebugContext(ctx, "worker overlay unavailable for source read", "worker_id", workerID, "err", err)
		return "", noop
	}
	return lease.Root, lease.Release
}

func (s *Handler) HandleBrowseProjectSource(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	workspaceID := p.WorkspaceID()
	listing, err := project.BrowseProjectSource(p, q.Get("root_id"), q.Get("dir"))
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	if !s.sourceWorkspaceStillCurrent(w, r, workspaceID, "") {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, toSourceDirListingDTO(listing))
}

func sourceTreeCursorScope(p *project.Project, rootID, dir string) string {
	var signature strings.Builder
	for _, root := range p.Roots {
		fmt.Fprintf(&signature, "%s\x00%s\x00", root.ID, filepath.Clean(root.Path))
	}
	return strings.TrimSpace(p.ID) + "\x00" + signature.String() + "\x00" + strings.TrimSpace(rootID) + "\x00" + strings.TrimSpace(dir)
}

func (s *Handler) beginProjectSourceMutation(
	w http.ResponseWriter,
	r *http.Request,
) (*project.Project, func(), bool) {
	if held, ok := r.Context().Value(sourceRequestProjectKey{}).(*project.Project); ok && held.ID == strings.TrimSpace(chi.URLParam(r, "id")) {
		return held, func() {}, true
	}
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return nil, nil, false
	}
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		release()
		return nil, nil, false
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		release()
		return nil, nil, false
	}
	return p, release, true
}

func (s *Handler) HandleReplaceProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.PutProjectSourceRequest
	if err := httpio.DecodeSourceJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	if strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.BaseSHA256) == "" || req.Encoding == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path, encoding, and base_sha256 are required")
		return
	}
	sessionID, turn := s.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.Write(r.Context(), operationID.String(), p, project.SourceWriteRequest{
		Path:       req.Path,
		RootID:     strings.TrimSpace(req.RootID),
		Content:    req.Content,
		Encoding:   string(req.Encoding),
		BaseSHA256: req.BaseSHA256,
		SessionID:  sessionID,
		Turn:       turn,
	})
	if errors.Is(err, project.ErrSourceBinary) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceBinaryDenied, "binary content cannot be written as text")
		return
	}
	if err != nil {
		s.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectSourceWriteResponse{
		Path:      result.Path,
		SizeBytes: result.SizeBytes,
		SHA256:    result.SHA256,
	})
}

func (s *Handler) HandleMakeProjectSourceEditable(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.MakeProjectSourceEditableRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.RootID) == "" || strings.TrimSpace(req.BaseSHA256) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path, root_id, and base_sha256 are required")
		return
	}
	result, err := project.MakeSourceEditable(p, project.SourceMakeEditableRequest{
		Path: req.Path, RootID: req.RootID, BaseSHA256: req.BaseSHA256,
	})
	if err != nil {
		s.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.MakeProjectSourceEditableResponse{
		Path: result.Path, RootID: result.RootID,
		PreviousMode: result.PreviousMode, Mode: result.Mode, Writable: result.Writable,
	})
}

func (s *Handler) HandleCreateProjectSourceEntry(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.CreateProjectSourceEntryRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path is required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID, turn := s.UserSourceChatAffiliation(r)
	rel, err := s.SourceMutations.Create(r.Context(), operationID.String(), p, project.SourceEntryCreateRequest{
		Path:      req.Path,
		Kind:      project.SourceEntryKind(strings.TrimSpace(req.Kind)),
		RootID:    strings.TrimSpace(req.RootID),
		SessionID: sessionID,
		Turn:      turn,
	})
	if err != nil {
		s.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.ProjectSourceEntryCreatedResponse{Path: rel})
}

func (s *Handler) HandleRenameProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.RenameProjectSourceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "from and to are required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID, turn := s.UserSourceChatAffiliation(r)
	var retargetID string
	result, err := s.SourceMutations.Rename(r.Context(), operationID.String(), p, project.SourceRenameRequest{
		RootID: strings.TrimSpace(req.RootID),
		From:   req.From,
		To:     req.To,
		Prepare: func(ctx context.Context, plan project.SourceRenamePlan) error {
			var prepareErr error
			retargetID, prepareErr = s.EditorDocuments.PrepareRetarget(ctx, p, plan)
			return prepareErr
		},
		SessionID: sessionID,
		Turn:      turn,
	})
	var retargetErr error
	if retargetID != "" {
		retargetErr = s.EditorDocuments.ReconcileRetarget(r.Context(), retargetID)
	}
	if err == nil {
		retargetErr = errors.Join(retargetErr, s.EditorDocuments.ReconcilePendingRetargets(r.Context()))
	}
	if err != nil {
		if retargetErr != nil {
			s.responses.Logger.ErrorContext(r.Context(), "reconcile editor documents after source rename failure", "error", retargetErr)
		}
		s.WriteProjectSourceError(w, r, err)
		return
	}
	if retargetErr != nil {
		s.responses.InternalError(w, r, retargetErr)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectSourceLifecycleResponse{
		RootID: result.RootID,
		Path:   result.Path,
	})
}

func (s *Handler) HandleCopyProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.CopyProjectSourceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "from and to are required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID, turn := s.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.Copy(r.Context(), operationID.String(), p, project.SourceCopyRequest{
		RootID:    strings.TrimSpace(req.RootID),
		From:      req.From,
		To:        req.To,
		SessionID: sessionID,
		Turn:      turn,
	})
	if err != nil {
		s.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectSourceLifecycleResponse{
		RootID: result.RootID,
		Path:   result.Path,
	})
}

func (s *Handler) HandleDeleteProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	q := r.URL.Query()
	pathQuery := q.Get("path")
	if pathQuery == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	recursive, _, queryErr := httpio.OptionalBoolQuery(r, "recursive")
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(q.Get("operation_id")))
	if err != nil {
		s.responses.InvalidQueryParam(w, "operation_id", "must be a UUID")
		return
	}
	sessionID, turn := s.UserSourceChatAffiliation(r)
	err = s.SourceMutations.Delete(r.Context(), operationID.String(), p, project.SourceDeleteRequest{
		RootID:    strings.TrimSpace(q.Get("root_id")),
		Path:      pathQuery,
		Recursive: recursive,
		SessionID: sessionID,
		Turn:      turn,
	})
	if err != nil {
		s.WriteProjectSourceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sourceIndexSnapshot returns the project id used by the poll clock.
func (s *Handler) sourceIndexSnapshot(w http.ResponseWriter, r *http.Request) (project.SourceIndexSnapshot, string, bool) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return project.SourceIndexSnapshot{}, "", false
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return project.SourceIndexSnapshot{}, "", false
	}
	if rootID := strings.TrimSpace(r.URL.Query().Get("root_id")); rootID != "" {
		scoped := *p
		scoped.Roots = nil
		for _, root := range p.Roots {
			if root.ID == rootID {
				scoped.Roots = append(scoped.Roots, root)
			}
		}
		if len(scoped.Roots) == 0 {
			s.writeSourceReadError(w, r, project.ErrSourceNoRoot)
			return project.SourceIndexSnapshot{}, "", false
		}
		p = &scoped
	}
	return s.sourceIndexes.Snapshot(r.Context(), p), p.ID, true
}

func sourceIndexCoverage(snapshot project.SourceIndexSnapshot) []wire.SourceIndexRootCoverage {
	out := make([]wire.SourceIndexRootCoverage, 0, len(snapshot.Coverage))
	for _, root := range snapshot.Coverage {
		out = append(out, wire.SourceIndexRootCoverage{RootID: root.RootID, State: sourceIndexWireState(root.State),
			DiscoveryComplete: root.DiscoveryComplete, Refreshing: root.Refreshing, BoundedDirectories: root.BoundedDirectories,
			FailedDirectories: root.FailedDirectories, Error: root.Error})
	}
	return out
}

func sourceIndexWireState(state project.SourceIndexState) wire.SourceIndexState {
	switch state {
	case project.SourceIndexReady:
		return wire.SourceIndexStateReady
	case project.SourceIndexFailed:
		return wire.SourceIndexStateFailed
	default:
		return wire.SourceIndexStateWarming
	}
}

func (s *Handler) HandleProjectSourceIndex(w http.ResponseWriter, r *http.Request) {
	snapshot, projectID, ok := s.sourceIndexSnapshot(w, r)
	if !ok {
		return
	}
	defer snapshot.Close()
	roots := make([]wire.SourceIndexRootSummary, 0, len(snapshot.Roots))
	for _, root := range snapshot.Roots {
		resources := make([]wire.SourceIndexResource, 0, len(root.Resources))
		for _, resource := range root.Resources {
			resources = append(resources, wire.SourceIndexResource{Kind: resource.Kind, Path: resource.Path})
		}
		roots = append(roots, wire.SourceIndexRootSummary{
			RootID: root.RootID, FileCount: root.FileCount, Resources: resources,
		})
	}
	status := http.StatusOK
	retryAfter := 0
	warmupKey := "source-index:" + projectID
	if snapshot.Refreshing {
		if snapshot.State == project.SourceIndexWarming {
			status = http.StatusAccepted
		}
		retryAfter = s.warmupPolls.warming(warmupKey)
	} else {
		s.warmupPolls.ready(warmupKey)
	}
	httpio.WriteJSON(w, status, wire.SourceIndexSummary{
		State: sourceIndexWireState(snapshot.State), Revision: snapshot.Revision,
		Refreshing: snapshot.Refreshing, RetryAfterMs: retryAfter, Coverage: sourceIndexCoverage(snapshot),
		FileCount: snapshot.FileCount, Roots: roots,
	})
}

func (s *Handler) HandleSearchProjectSource(w http.ResponseWriter, r *http.Request) {
	snapshot, projectID, ok := s.sourceIndexSnapshot(w, r)
	if !ok {
		return
	}
	defer snapshot.Close()
	pq, err := httpio.ReadPageQuery(r, sourceSearchLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	limit := pq.Limit
	query := r.URL.Query().Get("q")
	rootID := strings.TrimSpace(r.URL.Query().Get("root_id"))
	scope := sourceSearchScope(chi.URLParam(r, "id"), rootID, strings.TrimSpace(query))
	position, err := decodeSourceSearchCursor(pq.Cursor, scope, snapshot.Revision)
	if err != nil {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	style := project.HostSourcePathStyle()
	parsed := project.ParseSourceQuery(query, style)
	entries, err := project.SearchSourceIndex(r.Context(), snapshot, parsed, style, rootID, position, limit+1)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	hasMore := len(entries) > limit
	entries = entries[:min(limit, len(entries))]
	matches := make([]wire.SourceSearchMatch, 0, len(entries))
	for _, entry := range entries {
		highlights := make([]wire.SourceSearchHighlight, 0, len(entry.Highlights))
		for _, span := range entry.Highlights {
			highlights = append(highlights, wire.SourceSearchHighlight{Start: span.Start, End: span.End})
		}
		matches = append(matches, wire.SourceSearchMatch{RootID: entry.RootID, Path: entry.Path, Highlights: highlights})
	}
	status := http.StatusOK
	retryAfter := 0
	warmupKey := "source-search:" + projectID
	if snapshot.Refreshing {
		if snapshot.State == project.SourceIndexWarming {
			status = http.StatusAccepted
		}
		retryAfter = s.warmupPolls.warming(warmupKey)
	} else {
		s.warmupPolls.ready(warmupKey)
	}
	response := wire.SourceSearchResponse{
		State: sourceIndexWireState(snapshot.State), Revision: snapshot.Revision,
		Refreshing: snapshot.Refreshing, RetryAfterMs: retryAfter, Coverage: sourceIndexCoverage(snapshot), Matches: matches,
	}
	if parsed.Line > 0 {
		response.Location = &wire.SourceSearchLocation{Line: parsed.Line, Column: parsed.Column, EndLine: parsed.EndLine}
	}
	if outside, ok := snapshot.OutsidePath(parsed, style); ok {
		response.Outside = &wire.SourceSearchOutside{Path: outside.Path, Kind: "file"}
		if outside.Directory {
			response.Outside.Kind = "directory"
		}
	}
	if hasMore {
		nextCursor, err := encodeSourceSearchCursor(scope, snapshot.Revision, entries[len(entries)-1].SourceIndexEntry)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		response.NextCursor = nextCursor
	}
	httpio.WriteJSON(w, status, response)
}

func (s *Handler) HandleListProjectSourceSymbols(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	pathQuery := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathQuery == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	result, err := project.ListProjectSourceSymbols(r.Context(), p, project.SourceSymbolsRequest{
		Path:   pathQuery,
		RootID: strings.TrimSpace(r.URL.Query().Get("root_id")),
	})
	if err != nil {
		s.writeSourceAnalysisError(w, r, err)
		return
	}
	symbols := make([]wire.SourceSymbol, 0, len(result.Symbols))
	for _, sym := range result.Symbols {
		symbols = append(symbols, wire.SourceSymbol{
			Name: sym.Name,
			Kind: wire.SourceSymbolKind(sym.Kind),
			Line: sym.Line,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceSymbolsResponse{
		SHA256:    result.SHA256,
		Symbols:   symbols,
		Truncated: result.Truncated,
	})
}

func (s *Handler) HandleResolveProjectSourceDefinition(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	var req wire.SourceDefinitionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	pathQuery := strings.TrimSpace(req.Path)
	symbol := strings.TrimSpace(req.Symbol)
	if pathQuery == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path is required")
		return
	}
	if symbol == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "symbol is required")
		return
	}
	result, err := project.ResolveProjectSourceDefinitions(r.Context(), p, project.SourceDefinitionRequest{
		RootID:      strings.TrimSpace(req.RootID),
		Path:        pathQuery,
		Symbol:      symbol,
		Line:        req.Line,
		ExcludeDirs: SearchDependencyPatterns(),
	}, searchDeclarations)
	if err != nil {
		s.writeSourceAnalysisError(w, r, err)
		return
	}
	candidates := make([]wire.SourceDefinitionCandidate, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		candidates = append(candidates, wire.SourceDefinitionCandidate{
			RootID:  c.RootID,
			Path:    c.Path,
			Line:    c.Line,
			Kind:    wire.SourceSymbolKind(c.Kind),
			Snippet: c.Snippet,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceDefinitionResponse{
		Candidates: candidates,
		Truncated:  result.Truncated,
	})
}

// WriteProjectSourceError answers a failed source mutation: addressing, the
// write's own refusals, and the encoding a rewrite preserves.
func (s *Handler) WriteProjectSourceError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSourceAddressError(w, err) || s.writeSourceEncodingError(w, err) {
		return
	}
	var incomplete *project.SourceMoveIncompleteError
	switch {
	case errors.As(err, &incomplete):
		s.responses.Logger.WarnContext(r.Context(), "source move needs recovery", "held_path", incomplete.HeldPath, "err", incomplete.Cause)
		s.responses.FailDetails(w, wire.ApiErrorCodeSourceMoveIncomplete,
			map[string]any{"held_path": incomplete.HeldPath, "reason": "The source is retained at " + incomplete.HeldPath + "."},
			"the move needs recovery")
	case errors.Is(err, project.ErrSourceBusy):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathBusy, "a file operation is using this path; try again after it completes")
	case errors.Is(err, project.ErrSourceMutationConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for different source input")
	case errors.Is(err, project.ErrSourceMutationDiverged):
		s.responses.Fail(w, wire.ApiErrorCodeSourceMutationDiverged, "source changed while the operation was being recovered")
	case errors.Is(err, project.ErrSourceHistoryChanged):
		s.responses.Fail(w, wire.ApiErrorCodeSourceHistoryChanged, "file history changed; review the current files before trying again")
	case errors.Is(err, project.ErrSourceRecoveryFailed):
		s.responses.Logger.WarnContext(r.Context(), "source recovery data unavailable", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeSourceRecoveryFailed, "could not preserve file recovery data")
	case errors.Is(err, project.ErrSourceKindInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "kind must be file or folder")
	case errors.Is(err, project.ErrSourceExists):
		s.responses.Fail(w, wire.ApiErrorCodeSourceAlreadyExists, "a file or folder already exists at that path")
	case errors.Is(err, project.ErrSourceNotEmpty):
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotEmpty, "directory is not empty")
	case errors.Is(err, project.ErrSourceTrashFailed):
		s.responses.Logger.WarnContext(r.Context(), "source trash failed", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeSourceTrashFailed, "could not move to Trash")
	case errors.Is(err, project.ErrSourceWriteConflict):
		s.responses.Fail(w, wire.ApiErrorCodeSourceWriteConflict, "file changed on disk since it was loaded")
	case errors.Is(err, project.ErrSourceWriteTooLarge):
		s.responses.Fail(w, wire.ApiErrorCodeSourceContentTooLarge, "content exceeds the 4 MiB editor cap")
	case errors.Is(err, project.ErrSourcePermissionChange):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePermissionChangeFailed, "host filesystem refused the permission change")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// writeSourceReadError answers a failed source read: addressing, content the
// reader cannot present, and the raw image caps.
func (s *Handler) writeSourceReadError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSourceAddressError(w, err) || s.writeSourceEncodingError(w, err) {
		return
	}
	switch {
	case errors.Is(err, project.ErrSourceBinary):
		s.responses.Fail(w, wire.ApiErrorCodeSourceBinaryDenied, "binary files are not supported")
	case errors.Is(err, project.ErrSourceRawNotImage):
		s.responses.Fail(w, wire.ApiErrorCodeSourceRawNotImage, "raw source serves sniffed images only")
	case errors.Is(err, project.ErrSourceRawTooLarge):
		s.responses.Fail(w, wire.ApiErrorCodeSourceRawTooLarge, "image exceeds the 8 MiB raw cap")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// writeSourceAnalysisError answers a failed outline or definition analysis of
// a file the reader could open.
func (s *Handler) writeSourceAnalysisError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repomap.ErrDefinitionIncomplete):
		w.Header().Set("Retry-After", "30")
		s.responses.Fail(w, wire.ApiErrorCodeSourceAnalysisIncomplete, "the outline exceeded its analysis budget; the file is still available")
	case errors.Is(err, repomap.ErrDefinitionUnavailable):
		w.Header().Set("Retry-After", "30")
		s.responses.Fail(w, wire.ApiErrorCodeSourceAnalysisUnavailable, "the outline could not be analyzed; the file is still available")
	default:
		s.writeSourceReadError(w, r, err)
	}
}

// writeSourceAddressError answers the errors of resolving a project path.
func (s *Handler) writeSourceAddressError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, project.ErrSourcePathInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path is required")
	case errors.Is(err, project.ErrSourceNoRoot):
		s.responses.Fail(w, wire.ApiErrorCodeNoProjectRoot, "project has no attached folder")
	case errors.Is(err, project.ErrSourceCrossRoot):
		s.responses.Fail(w, wire.ApiErrorCodeSourceCrossRoot, "source paths must stay in the same project root")
	case errors.Is(err, project.ErrSourcePathProtected):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathProtected, "path is protected project metadata")
	case errors.Is(err, project.ErrSourcePathDenied):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathDenied, "path is outside the project sandbox")
	case errors.Is(err, project.ErrSourceNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotFound, "file not found")
	default:
		return false
	}
	return true
}

// writeSourceEncodingError answers a text encoding the host cannot decode or
// a requested encoding it does not support.
func (s *Handler) writeSourceEncodingError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, project.ErrSourceUnsupportedEncoding):
		detected := textfile.Unknown
		var ue *project.SourceUnsupportedEncodingError
		if errors.As(err, &ue) && ue.Detected != "" {
			detected = ue.Detected
		}
		s.responses.FailDetails(w, wire.ApiErrorCodeUnsupportedEncoding, map[string]any{"detected": detected}, "unsupported text encoding")
	case errors.Is(err, project.ErrSourceEncodingInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "encoding must be a supported UTF-8 or UTF-16 variant")
	case errors.Is(err, project.ErrSourceDecodeAsInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "decode_as must be utf-16le or utf-16be for an unsupported file")
	default:
		return false
	}
	return true
}

func toSourceDirListingDTO(l project.SourceDirListing) wire.SourceDirListing {
	entries := make([]wire.SourceDirEntry, 0, len(l.Entries))
	for _, e := range l.Entries {
		entries = append(entries, wire.SourceDirEntry{Name: e.Name, IsDir: e.IsDir})
	}
	return wire.SourceDirListing{
		WorkspaceID: l.WorkspaceID, RootID: l.RootID, Dir: l.Dir,
		WatchComplete: l.WatchComplete, Entries: entries,
	}
}

func ToProjectSourceReadDTO(
	r *project.SourceReadResult,
	workspaceID string,
	workspaceKind wire.SourceWorkspaceKind,
) wire.ProjectSourceReadResponse {
	out := wire.ProjectSourceReadResponse{
		FileID:        r.FileID,
		VersionID:     r.VersionID,
		WorkspaceID:   workspaceID,
		WorkspaceKind: workspaceKind,
		Path:          r.Path,
		Language:      filekind.LanguageForPath(r.Path),
		Content:       r.Content,
		OverLimit:     r.OverLimit,
		Binary:        r.Binary,
		SizeBytes:     r.SizeBytes,
		MIME:          r.MIME,
		SHA256:        r.SHA256,
		RootID:        r.RootID,
		Writable:      r.Writable,
	}
	if r.Encoding != "" {
		out.Encoding = wire.SourceEncoding(r.Encoding)
	}
	if !r.MTime.IsZero() {
		out.ModifiedAt = r.MTime.UTC().Format(time.RFC3339)
	}
	return out
}
