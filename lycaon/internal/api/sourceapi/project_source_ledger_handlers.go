package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *History) HandleListProjectSourceVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	req, ok := s.parseSourceVersionsQuery(w, r, p.ID)
	if !ok {
		return
	}
	versions := []wire.SourceFileVersion{}
	var next sourceVersionsPosition
	if req.readRetained {
		result, err := s.SourceLedger.QueryFileVersions(
			r.Context(), p.ID, req.fileID, req.limit, req.from.RetainedBefore,
		)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		versions = make([]wire.SourceFileVersion, 0, len(result.Versions))
		for _, version := range result.Versions {
			versions = append(versions, MapSourceVersion(version, result.GitTransitions, result.CommandWindows))
		}
		next.RetainedBefore = result.NextBeforeOrdinal
	}
	current := wire.SourceTip{State: wire.SourceTipStateAbsent}
	head, _, err := s.Workspace.workspaceSourceHead(r.Context(), p, req.fileID)
	headKnown := err == nil
	if headKnown && (head.State == string(wire.SourceTipStateContent) || head.State == string(wire.SourceTipStateAbsent)) {
		current = wire.SourceTip{State: wire.SourceTipState(head.State), Sha256: head.SHA256}
	} else if err != nil && !errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.InternalError(w, r, err)
		return
	}
	out := wire.SourceFileVersionsResponse{
		FileID: req.fileID, Current: current, Versions: versions,
		Commits: []wire.SourceFileCommit{}, Arrivals: []wire.SourceGitChange{},
		GitHistoryState: wire.SourceGitHistoryStateNotRequested,
	}
	// Repository history requires a tracked primary location.
	if req.readGit {
		out.GitHistoryState = wire.SourceGitHistoryStateNotTracked
		if headKnown {
			out.Commits, out.Arrivals, out.GitHistoryState, next.GitSkip =
				s.Review.fileGitLane(r.Context(), p, req.fileID, head, req.from.GitSkip)
		}
	}
	if next.RetainedBefore > 0 || next.GitSkip > 0 {
		out.NextCursor, err = sourceVersionPages.Encode(req.scope, next)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	if since, tracked, sinceErr := s.SourceLedger.FileTrackedSince(r.Context(), p.ID, req.fileID); sinceErr == nil && tracked {
		out.TrackedAt = &since
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// sourceVersionsPosition continues each lane of one file's history. Zero
// marks a lane the listing exhausted; a first page carries no position.
type sourceVersionsPosition struct {
	RetainedBefore int64 `json:"retained_before,omitempty"`
	GitSkip        int   `json:"git_skip,omitempty"`
}

var (
	sourceVersionPages     = pagecursor.For[sourceVersionsPosition]("source_versions")
	sourceVersionPageLimit = httpio.MustPageLimit(100, 1, 500)
)

// sourceVersionsQuery is one page request over the lanes it reads.
type sourceVersionsQuery struct {
	fileID, scope         string
	limit                 int
	from                  sourceVersionsPosition
	readRetained, readGit bool
}

func (s *History) parseSourceVersionsQuery(w http.ResponseWriter, r *http.Request, projectID string) (sourceVersionsQuery, bool) {
	fileID := strings.TrimSpace(r.URL.Query().Get("file_id"))
	if fileID == "" {
		s.responses.InvalidQueryParam(w, "file_id", "is required")
		return sourceVersionsQuery{}, false
	}
	page, err := httpio.ReadPageQuery(r, sourceVersionPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return sourceVersionsQuery{}, false
	}
	lane, present, err := httpio.SingleQueryValue(r, "lane")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return sourceVersionsQuery{}, false
	}
	if !present {
		lane = "all"
	}
	if lane != "all" && lane != "retained" && lane != "git" {
		s.responses.InvalidQueryParam(w, "lane", "must be all, retained, or git")
		return sourceVersionsQuery{}, false
	}
	q := sourceVersionsQuery{
		fileID: fileID, limit: page.Limit,
		scope:        pagecursor.Scope(projectID, strings.TrimSpace(r.URL.Query().Get("session_id")), fileID, lane),
		readRetained: lane != "git", readGit: lane != "retained",
	}
	if page.Cursor == "" {
		return q, true
	}
	q.from, err = sourceVersionPages.Decode(page.Cursor, q.scope)
	if err == nil && q.from.RetainedBefore <= 0 && q.from.GitSkip <= 0 {
		err = pagecursor.ErrInvalid
	}
	if err != nil {
		s.responses.PageCursorError(w, r, "cursor", err)
		return sourceVersionsQuery{}, false
	}
	// A continuation reads only the lanes that still hold pages.
	q.readRetained = q.readRetained && q.from.RetainedBefore > 0
	q.readGit = q.readGit && q.from.GitSkip > 0
	return q, true
}

func (s *Mutations) HandleRestoreProjectSourceVersion(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	versionID := strings.TrimSpace(chi.URLParam(r, "version_id"))
	if versionID == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "version_id is required")
		return
	}
	var req wire.SourceVersionRestoreRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	if strings.TrimSpace(req.FileID) == "" || strings.TrimSpace(req.RootID) == "" || strings.TrimSpace(req.Path) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "file_id, root_id, and path are required")
		return
	}
	version, err := s.SourceLedger.ReadRestorableVersion(r.Context(), p.ID, versionID)
	blobUnavailable := errors.Is(err, sourceledger.ErrVersionUnavailable)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionNotFound, "source version not found")
		return
	}
	if err != nil && !blobUnavailable {
		s.responses.InternalError(w, r, err)
		return
	}
	// Repository-caused states use verified repository bytes.
	if fromGit, ok := s.Comparisons.gitSourcedVersion(r.Context(), p, versionID); ok {
		version, blobUnavailable = fromGit, false
	}
	if blobUnavailable {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable, "exact content for this version is unavailable")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.Versions.Restore(r.Context(), operationID.String(), p, projectsource.SourceVersionRestoreRequest{
		Version: version, FileID: strings.TrimSpace(req.FileID), RootID: strings.TrimSpace(req.RootID),
		Path: req.Path, Base: req.Base, SessionID: sessionID, Turn: turn,
	})
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionNotFound, "source version not found")
		return
	}
	if errors.Is(err, sourceledger.ErrVersionUnavailable) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable, "exact content for this version is unavailable")
		return
	}
	if err != nil {
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceVersionRestoreResponse{
		VersionID: result.VersionID, PreviousVersionID: result.PreviousVersionID,
		FileID: result.FileID, RootID: result.RootID,
		Path: result.Path, State: result.State, Sha256: result.SHA256, Changed: result.Changed,
	})
}

// gitSourcedVersion resolves and verifies repository-caused bytes.
func (s *Comparisons) gitSourcedVersion(
	ctx context.Context,
	p *project.Project,
	versionID string,
) (sourceledger.RestorableVersion, bool) {
	src, err := s.SourceLedger.ReadVersionGitSource(ctx, p.ID, versionID)
	if err != nil || src.State != "content" || src.SHA256 == "" {
		return sourceledger.RestorableVersion{}, false
	}
	mgr := s.Git.Manager()
	rootAbs, prefix, ok := s.resolveRootRepoPosition(ctx, p, src.RootID)
	if !ok {
		return sourceledger.RestorableVersion{}, false
	}
	rel := strings.TrimPrefix(filepath.ToSlash(src.Path), "/")
	res, err := mgr.Show(ctx, rootAbs, git.GitShowOpts{
		Ref: src.Commit, Path: prefix + rel,
		// One spare byte detects truncation.
		MaxBytes: int(src.ByteSize) + 1,
	})
	if err != nil || res == nil || res.Truncated {
		return sourceledger.RestorableVersion{}, false
	}
	raw := []byte(res.Content)
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != src.SHA256 {
		return sourceledger.RestorableVersion{}, false
	}
	return sourceledger.RestorableVersion{
		ID: src.VersionID, FileID: src.FileID, ProjectID: p.ID,
		RootID: src.RootID, Path: src.Path, State: src.State,
		SHA256: src.SHA256, Content: raw,
	}, true
}

// Repository prefixes include the trailing slash for nested roots.
func (s *Comparisons) resolveRootRepoPosition(
	ctx context.Context,
	p *project.Project,
	rootID string,
) (string, string, bool) {
	roots := project.RootRefsFrom(p)
	rootPath := ""
	for _, root := range roots {
		if root.ID == rootID {
			rootPath = root.Path
			break
		}
	}
	if strings.TrimSpace(rootPath) == "" {
		return "", "", false
	}
	repos, err := s.Git.LoadOrderedRepos(ctx, p, roots, "", "")
	if err != nil {
		return "", "", false
	}
	toplevel := ""
	for _, repo := range repos {
		if !repo.Available || strings.TrimSpace(repo.Toplevel) == "" {
			continue
		}
		for _, id := range repo.RootIDs {
			if id == rootID {
				toplevel = repo.Toplevel
			}
		}
	}
	if toplevel == "" {
		return "", "", false
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return "", "", false
	}
	abs = filepath.Clean(abs)
	return abs, RootPrefixInRepo(toplevel, abs), true
}

func MapSourceVersion(
	version sourceledger.Version,
	transitions map[string]sourceledger.GitTransition,
	windows map[string]sourceledger.CommandWindow,
) wire.SourceFileVersion {
	out := wire.SourceFileVersion{
		Contributors: mapSourceContributors(version.Contributors),
		ID:           version.ID, FileID: version.FileID,
		WorkspaceKind: version.BranchID.Kind(), RootID: version.RootID, Path: version.Path,
		State: version.State, ContentSha256: version.ContentSHA256,
		SizeBytes: version.ByteSize, CaptureState: version.CaptureState,
		CaptureReason: version.CaptureReason, CaptureQuality: version.CaptureQuality,
		Landing: version.Landing,
		Ordinal: version.Ordinal, Turn: version.Turn, CreatedAt: version.CreatedTS,
	}
	// A state no effect produced omits action fields rather than emptying them.
	if version.OperationID != "" {
		out.OperationID = &version.OperationID
	}
	if version.EffectID != "" {
		out.EffectID = &version.EffectID
	}
	if version.Op != "" {
		op := version.Op
		out.Op = &op
	}
	if version.Origin != "" {
		origin := version.Origin
		out.Origin = &origin
	}
	if version.Cause != "" {
		out.Cause = &version.Cause
	}
	if version.ParentVersionID != "" {
		out.ParentVersionID = &version.ParentVersionID
	}
	if version.DerivedFromVersionID != "" {
		out.DerivedFromVersionID = &version.DerivedFromVersionID
	}
	if version.ActorLabel != "" {
		out.ActorLabel = &version.ActorLabel
	}
	if version.SessionID != "" {
		out.SessionID = &version.SessionID
	}
	if version.JobID != "" {
		out.WorkerID = &version.JobID
	}
	if version.ToolCallID != "" {
		out.ToolCallID = &version.ToolCallID
	}
	if version.BatchID != "" {
		out.BatchID = &version.BatchID
	}
	if transition, ok := transitions[version.GitTransitionID]; ok {
		out.GitChange = mapSourceGitChange(transition)
	}
	if window, ok := windows[version.CommandWindowID]; ok {
		out.Command = mapSourceCommandWindow(window)
	}
	return out
}

// HandleGetProjectSourceComparison serves exact range, effect, and version comparisons.
