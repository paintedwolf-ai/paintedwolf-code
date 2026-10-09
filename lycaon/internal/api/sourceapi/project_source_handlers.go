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
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceViewerWorkspace struct {
	id     string
	kind   wire.SourceWorkspaceKind
	branch sourcebranch.ID
}

func (s *Workspace) HandleGetProjectSource(w http.ResponseWriter, r *http.Request) {
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
	p, err = requestscope.SourceProjectInBranch(p, branchRoot)
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
	observation, err := projectsource.ObserveProjectSource(p, req)
	if errors.Is(err, projectsource.ErrSourceNotFound) && includeDeleted && (workerID == "" || branch.IsWorker()) {
		var deleted *wire.ProjectSourceReadResponse
		deleted, err = s.readDeletedSource(r.Context(), p, req, scope)
		if err == nil {
			httpio.WriteJSON(w, http.StatusOK, deleted)
			return
		}
		// A replacement that arrived during history lookup takes the current read path.
		if errors.Is(err, projectsource.ErrSourceExists) {
			observation, err = projectsource.ObserveProjectSource(p, req)
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

func sourceViewerRequest(r *http.Request) (projectsource.SourceReadRequest, bool, error) {
	req := projectsource.SourceReadRequest{
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
func (s *Workspace) projectSourceObservation(ctx context.Context, projectID string, observation *projectsource.SourceReadObservation, scope sourceViewerWorkspace) (wire.ProjectSourceReadResponse, error) {
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

func (s *Workspace) recordedSourceObservation(ctx context.Context, projectID string, observation *projectsource.SourceReadObservation, scope sourceViewerWorkspace) (sourceledger.TrackedFile, error) {
	return s.SourceLedger.LookupFile(ctx, sourceledger.TrackInput{
		ProjectID: projectID, BranchID: scope.branch,
		RootID: observation.RootID, Path: observation.Path, EntryKind: sourceledger.EntryKindFile,
		SHA256: observation.Revision.SHA256, Content: observation.Revision.Bytes, Size: observation.SizeBytes,
	})
}

func (s *Workspace) HandleGetSourceWorkspace(w http.ResponseWriter, r *http.Request) {
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
	inventory, err := s.Watch.sourceInventoryState(r.Context(), p)
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
		State: wire.SourceWatchLive, Recursive: coverage.Recursive, UnwatchedDirectories: coverage.Truncated, PolicyUnwatched: coverage.PolicyUnwatched,
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
func (s *Workspace) sourceReadWorkspace(
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

func (s *Workspace) writeSourceWorkspaceMismatch(
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

func (s *Workspace) currentSourceWorkspaceID(
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

func (s *Workspace) sourceWorkspaceStillCurrent(
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

func (s *Workspace) HandleGetProjectSourceRaw(w http.ResponseWriter, r *http.Request) {
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
	p, err := requestscope.SourceProjectInBranch(p, branchRoot)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	result, err := projectsource.ReadProjectSourceRaw(p, projectsource.SourceReadRequest{
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
func (s *Workspace) WorkerBranchRoot(ctx context.Context, projectID, rawWorkerID string) (string, func()) {
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

func (s *Workspace) HandleBrowseProjectSource(w http.ResponseWriter, r *http.Request) {
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
	listing, err := projectsource.BrowseProjectSource(p, q.Get("root_id"), q.Get("dir"))
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

func (s *Workspace) WriteProjectSourceError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSourceAddressError(w, err) || httpio.WriteSourceEncodingError(s.responses, w, err) {
		return
	}
	var incomplete *projectsource.SourceMoveIncompleteError
	switch {
	case errors.As(err, &incomplete):
		s.responses.Logger.WarnContext(r.Context(), "source move needs recovery", "held_path", incomplete.HeldPath, "err", incomplete.Cause)
		s.responses.FailDetails(w, wire.ApiErrorCodeSourceMoveIncomplete,
			map[string]any{"held_path": incomplete.HeldPath, "reason": "The source is retained at " + incomplete.HeldPath + "."},
			"the move needs recovery")
	case errors.Is(err, projectsource.ErrSourceBusy):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathBusy, "a file operation is using this path; try again after it completes")
	case errors.Is(err, projectsource.ErrSourceMutationConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for different source input")
	case errors.Is(err, projectsource.ErrSourceMutationDiverged):
		s.responses.Fail(w, wire.ApiErrorCodeSourceMutationDiverged, "source changed while the operation was being recovered")
	case errors.Is(err, projectsource.ErrSourceHistoryChanged):
		s.responses.Fail(w, wire.ApiErrorCodeSourceHistoryChanged, "file history changed; review the current files before trying again")
	case errors.Is(err, projectsource.ErrSourceRecoveryFailed):
		s.responses.Logger.WarnContext(r.Context(), "source recovery data unavailable", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeSourceRecoveryFailed, "could not preserve file recovery data")
	case errors.Is(err, projectsource.ErrSourceKindInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "kind must be file or folder")
	case errors.Is(err, projectsource.ErrSourceExists):
		s.responses.Fail(w, wire.ApiErrorCodeSourceAlreadyExists, "a file or folder already exists at that path")
	case errors.Is(err, projectsource.ErrSourceNotEmpty):
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotEmpty, "directory is not empty")
	case errors.Is(err, projectsource.ErrSourceTrashFailed):
		s.responses.Logger.WarnContext(r.Context(), "source trash failed", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeSourceTrashFailed, "could not move to Trash")
	case errors.Is(err, projectsource.ErrSourceWriteConflict):
		s.responses.Fail(w, wire.ApiErrorCodeSourceWriteConflict, "file changed on disk since it was loaded")
	case errors.Is(err, projectsource.ErrSourceWriteTooLarge):
		s.responses.Fail(w, wire.ApiErrorCodeSourceContentTooLarge, "content exceeds the 4 MiB editor cap")
	case errors.Is(err, projectsource.ErrSourcePermissionChange):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePermissionChangeFailed, "host filesystem refused the permission change")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// writeSourceReadError answers a failed source read: addressing, content the
// reader cannot present, and the raw image caps.
func (s *Workspace) writeSourceReadError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSourceAddressError(w, err) || httpio.WriteSourceEncodingError(s.responses, w, err) {
		return
	}
	switch {
	case errors.Is(err, projectsource.ErrSourceBinary):
		s.responses.Fail(w, wire.ApiErrorCodeSourceBinaryDenied, "binary files are not supported")
	case errors.Is(err, projectsource.ErrSourceRawNotImage):
		s.responses.Fail(w, wire.ApiErrorCodeSourceRawNotImage, "raw source serves sniffed images only")
	case errors.Is(err, projectsource.ErrSourceRawTooLarge):
		s.responses.Fail(w, wire.ApiErrorCodeSourceRawTooLarge, "image exceeds the 8 MiB raw cap")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// writeSourceAnalysisError answers a failed outline or definition analysis of
// a file the reader could open.
func (s *Analysis) writeSourceAnalysisError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repomap.ErrDefinitionIncomplete):
		w.Header().Set("Retry-After", "30")
		s.responses.Fail(w, wire.ApiErrorCodeSourceAnalysisIncomplete, "the outline exceeded its analysis budget; the file is still available")
	case errors.Is(err, repomap.ErrDefinitionUnavailable):
		w.Header().Set("Retry-After", "30")
		s.responses.Fail(w, wire.ApiErrorCodeSourceAnalysisUnavailable, "the outline could not be analyzed; the file is still available")
	default:
		s.Workspace.writeSourceReadError(w, r, err)
	}
}

// writeSourceAddressError answers the errors of resolving a project path.
func (s *Workspace) writeSourceAddressError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, projectsource.ErrSourcePathInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path is required")
	case errors.Is(err, projectsource.ErrSourceNoRoot):
		s.responses.Fail(w, wire.ApiErrorCodeNoProjectRoot, "project has no attached folder")
	case errors.Is(err, projectsource.ErrSourceCrossRoot):
		s.responses.Fail(w, wire.ApiErrorCodeSourceCrossRoot, "source paths must stay in the same project root")
	case errors.Is(err, projectsource.ErrSourcePathProtected):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathProtected, "path is protected project metadata")
	case errors.Is(err, projectsource.ErrSourcePathDenied):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathDenied, "path is outside the project sandbox")
	case errors.Is(err, projectsource.ErrSourceNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSourceNotFound, "file not found")
	default:
		return false
	}
	return true
}

// writeSourceEncodingError answers a text encoding the host cannot decode or
// a requested encoding it does not support.

func toSourceDirListingDTO(l projectsource.SourceDirListing) wire.SourceDirListing {
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
	r *projectsource.SourceReadResult,
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
