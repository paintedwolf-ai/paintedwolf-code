package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// gitWorkingTreeAdapter translates repository paths to project roots.
type gitWorkingTreeAdapter struct {
	mgr  git.GitManager
	tops map[string]string
}

// Unmapped roots are distinct from roots at the repository top.
func (g gitWorkingTreeAdapter) repoPrefix(rootAbs string) (string, bool) {
	toplevel, known := g.tops[rootAbs]
	if !known {
		return "", false
	}
	return RootPrefixInRepo(toplevel, rootAbs), true
}

// TreeOIDs resolves current blob ids for root-relative paths.
func (g gitWorkingTreeAdapter) TreeOIDs(ctx context.Context, rootAbs string, paths []string) (map[string]string, bool) {
	if g.mgr == nil {
		return nil, false
	}
	relPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		relPaths = append(relPaths, strings.TrimPrefix(filepath.ToSlash(path), "/"))
	}
	oids, err := g.mgr.TreeOIDs(ctx, rootAbs, "HEAD", relPaths)
	if err != nil {
		// Unknown is not an empty tree.
		return nil, false
	}
	return oids, true
}

// RootPrefixInRepo returns a root's slash-terminated repository prefix.
func RootPrefixInRepo(toplevel, rootAbs string) string {
	toplevel = strings.TrimSpace(toplevel)
	rootAbs = strings.TrimSpace(rootAbs)
	if toplevel == "" || rootAbs == "" {
		return ""
	}
	rel, err := filepath.Rel(toplevel, rootAbs)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel + "/"
}

func (s *Handler) HandleListProjectSourceWalk(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	query, ok := s.parseSourceWalkQuery(w, r)
	if !ok {
		return
	}
	bas := query.baseline
	p, sessionID, ok := s.scopeProjectForWalk(w, r, p, bas)
	if !ok {
		return
	}
	if query.currentTurn {
		turn, err := s.SessionStore.UserTurnOrdinal(r.Context(), bas.SessionID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		bas.Turn = turn
	}
	scope := sourceWalkScope(p.ID, sessionID, bas)
	if bas.Kind == sourceledger.BaselineCommit {
		s.writeCommitReview(w, r, p, scope, query.page)
		return
	}
	var beforeOrdinal int64
	if query.page.Cursor != "" {
		position, err := sourceWalkPages.Decode(query.page.Cursor, scope)
		if err == nil && position.BeforeOrdinal <= 0 {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		beforeOrdinal = position.BeforeOrdinal
	}
	bas.RootBranches = workspaceSourceBranches(p)
	res, err := s.SourceLedger.QueryWalk(
		r.Context(), p.ID, bas, query.page.Limit, beforeOrdinal, s.CommitLens(r.Context(), p),
	)
	if errors.Is(err, sourceledger.ErrBaselinePinNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourcePinNotFound, "baseline pin not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := MapSourceWalk(res)
	if res.NextBeforeOrdinal > 0 {
		out.NextCursor, err = sourceWalkPages.Encode(scope, sourceWalkPosition{BeforeOrdinal: res.NextBeforeOrdinal})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// sourceWalkPosition continues a recorded range below one effect ordinal.
type sourceWalkPosition struct {
	BeforeOrdinal int64 `json:"before_ordinal"`
}

var (
	sourceWalkPages     = pagecursor.For[sourceWalkPosition]("source_walk")
	sourceWalkPageLimit = httpio.MustPageLimit(100, 1, 500)
)

// sourceWalkScope binds a cursor to the workspace and resolved range a page
// answered; a current-turn baseline binds to the turn it resolved to.
func sourceWalkScope(projectID, sessionID string, bas sourceledger.Baseline) string {
	return pagecursor.Scope(projectID, sessionID, bas.String(),
		strconv.FormatBool(bas.WithOutsideChanges), strconv.FormatBool(bas.WithoutUserEdits))
}

// sourceWalkQuery is a walk request whose shape is valid; the chat it names
// has not been looked up yet.
type sourceWalkQuery struct {
	baseline sourceledger.Baseline
	// currentTurn asks for the chat's current turn, resolved at read time.
	currentTurn bool
	page        httpio.PageQuery
}

func (s *Handler) parseSourceWalkQuery(w http.ResponseWriter, r *http.Request) (sourceWalkQuery, bool) {
	raw := r.URL.Query().Get("baseline")
	bas, currentTurn := sourceledger.ParseCurrentTurn(raw)
	if !currentTurn {
		var err error
		if bas, err = sourceledger.ParseBaseline(raw); err != nil {
			s.responses.InvalidQuery(w, &httpio.QueryParameterError{Parameter: "baseline", Reason: "is not a source baseline"})
			return sourceWalkQuery{}, false
		}
	}
	page, err := httpio.ReadPageQuery(r, sourceWalkPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return sourceWalkQuery{}, false
	}
	if outside, present, err := httpio.OptionalBoolQuery(r, "include_outside_changes"); err != nil {
		s.responses.InvalidQuery(w, err)
		return sourceWalkQuery{}, false
	} else if present && outside {
		if bas.Kind != sourceledger.BaselineSession {
			s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "include_outside_changes applies to a session baseline")
			return sourceWalkQuery{}, false
		}
		bas.WithOutsideChanges = true
	}
	if mark, present, err := httpio.OptionalBoolQuery(r, "mark_user_edits"); err != nil {
		s.responses.InvalidQuery(w, err)
		return sourceWalkQuery{}, false
	} else if present && !mark {
		if bas.Kind == sourceledger.BaselineCommit {
			s.responses.InvalidQueryParam(w, "mark_user_edits",
				"cannot be false for a commit baseline, because Git records no author")
			return sourceWalkQuery{}, false
		}
		bas.WithoutUserEdits = true
	}
	return sourceWalkQuery{baseline: bas, currentTurn: currentTurn, page: page}, true
}

// scopeProjectForWalk reads a chat baseline in that chat's workspace, so the
// range and its tips come from the branch the chat writes to. It returns the
// chat whose workspace answers, or empty for the project's own roots.
func (s *Handler) scopeProjectForWalk(w http.ResponseWriter, r *http.Request, p *project.Project, bas sourceledger.Baseline) (*project.Project, string, bool) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if bas.NamesChat() {
		if sessionID != "" && sessionID != bas.SessionID {
			s.responses.InvalidQueryParam(w, "session_id", "must name the chat the baseline reads")
			return nil, "", false
		}
		sessionID = bas.SessionID
	}
	scoped, ok := requestscope.ProjectForSession(s.SessionStore, s.responses, w, r, p, sessionID)
	return scoped, sessionID, ok
}

// MapSourceWalk maps one page; the handler seals its continuation.
func MapSourceWalk(res sourceledger.WalkResult) wire.SourceWalkResponse {
	out := wire.SourceWalkResponse{
		Baseline:        res.Baseline.String(),
		Turns:           append([]wire.SourceWalkTurn{}, res.Turns...),
		CommitAvailable: res.CommitAvailable,
		Files:           make([]wire.SourceWalkFile, 0, len(res.Files)),
		GitChanges:      make([]wire.SourceGitChange, 0, len(res.GitChanges)),
		Commands:        make([]wire.SourceCommandWindow, 0, len(res.Commands)),
	}
	// Effects omit references absent from the page's lookup lists.
	listed := make(map[string]struct{}, len(res.GitChanges))
	for _, transition := range res.GitChanges {
		listed[transition.ID] = struct{}{}
		out.GitChanges = append(out.GitChanges, *mapSourceGitChange(transition))
	}
	listedCommands := make(map[string]struct{}, len(res.Commands))
	for _, window := range res.Commands {
		listedCommands[window.ID] = struct{}{}
		out.Commands = append(out.Commands, *mapSourceCommandWindow(window))
	}
	for _, f := range res.Files {
		gf := wire.SourceWalkFile{
			FileID:                  f.FileID,
			RootID:                  f.RootID,
			Path:                    f.Path,
			ChangedSincePresented:   f.ChangedSincePresented,
			UnpresentedAgentEffects: f.UnpresentedAgentEffects,
			PresentationEffectID:    f.PresentationEffectID,
			PresentationOrdinal:     f.PresentationOrdinal,
			Tip:                     wire.SourceTip{State: f.Tip.State, Sha256: f.Tip.SHA256},
			HeadMatch:               f.HeadMatch,
			Effects:                 make([]wire.SourceWalkEffect, 0, len(f.Effects)),
		}
		if !f.LastTS.IsZero() {
			t := f.LastTS
			gf.LastAt = &t
		}
		for _, c := range f.Effects {
			row := MapSourceEffect(c)
			if _, ok := listed[c.GitTransitionID]; ok {
				id := c.GitTransitionID
				row.GitChangeID = &id
			}
			if _, ok := listedCommands[c.CommandWindowID]; ok {
				id := c.CommandWindowID
				row.CommandID = &id
			}
			gf.Effects = append(gf.Effects, row)
		}
		out.Files = append(out.Files, gf)
	}
	return out
}

func mapSourceCommandWindow(w sourceledger.CommandWindow) *wire.SourceCommandWindow {
	out := &wire.SourceCommandWindow{
		ID: w.ID, SessionID: w.SessionID, Turn: w.Turn,
		CommandLine: w.CommandLine, State: w.State, Ordinal: w.Ordinal, StartedAt: w.StartedTS,
	}
	if w.ToolName != "" {
		name := w.ToolName
		out.ToolName = &name
	}
	if w.ToolCallID != "" {
		id := w.ToolCallID
		out.ToolCallID = &id
	}
	if w.AdmissionMode != "" {
		mode := w.AdmissionMode
		out.AdmissionMode = &mode
	}
	if !w.EndedTS.IsZero() {
		ended := w.EndedTS
		out.EndedAt = &ended
	}
	return out
}

func mapSourceGitChange(t sourceledger.GitTransition) *wire.SourceGitChange {
	out := &wire.SourceGitChange{
		ID: t.ID, RootID: t.RootID, Kind: wire.SourceGitChangeKind(t.Kind),
		Ordinal: t.Ordinal, ObservedAt: t.ObservedTS,
		SessionID: t.SessionID, Turn: t.Turn, ToolCallID: t.ToolCallID, ToolName: t.ToolName,
	}
	assign := func(dst **string, value string) {
		if value != "" {
			*dst = &value
		}
	}
	assign(&out.FromCommit, t.FromCommit)
	assign(&out.ToCommit, t.ToCommit)
	assign(&out.FromRef, t.FromRef)
	assign(&out.ToRef, t.ToRef)
	assign(&out.Detail, t.Detail)
	return out
}

func MapSourceEffect(c sourceledger.Effect) wire.SourceWalkEffect {
	row := wire.SourceWalkEffect{
		ID: c.ID, ProjectID: c.ProjectID, OperationID: c.OperationID, FileID: c.FileID,
		AfterVersionID: c.AfterVersionID,
		WorkspaceKind:  c.BranchID.Kind(), RootID: c.RootID, Path: c.Path,
		EntryKind: c.EntryKind,
		Op:        c.Op, Origin: c.Origin, Turn: c.Turn, ObservedAt: c.TS,
		Ordinal: c.Ordinal, Cause: c.Cause,
		CaptureQuality: c.CaptureQuality, Contributors: mapSourceContributors(c.Contributors),
	}
	if c.BeforeVersionID != "" {
		row.BeforeVersionID = &c.BeforeVersionID
	}
	if c.ActorLabel != "" {
		row.ActorLabel = &c.ActorLabel
	}
	if c.FromPath != "" {
		row.FromPath = &c.FromPath
	}
	if c.FromRootID != "" {
		row.FromRootID = &c.FromRootID
	}
	if c.SessionID != "" {
		row.SessionID = &c.SessionID
	}
	if c.JobID != "" {
		row.WorkerID = &c.JobID
	}
	if c.ToolCallID != "" {
		row.ToolCallID = &c.ToolCallID
	}
	if c.ToolName != "" {
		row.ToolName = &c.ToolName
	}
	if c.BatchID != "" {
		row.BatchID = &c.BatchID
	}
	return row
}

func (s *Handler) HandleListProjectSourceVersions(w http.ResponseWriter, r *http.Request) {
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
	head, _, err := s.workspaceSourceHead(r.Context(), p, req.fileID)
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
				s.fileGitLane(r.Context(), p, req.fileID, head, req.from.GitSkip)
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

func (s *Handler) parseSourceVersionsQuery(w http.ResponseWriter, r *http.Request, projectID string) (sourceVersionsQuery, bool) {
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

func (s *Handler) HandleRestoreProjectSourceVersion(w http.ResponseWriter, r *http.Request) {
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
	if fromGit, ok := s.gitSourcedVersion(r.Context(), p, versionID); ok {
		version, blobUnavailable = fromGit, false
	}
	if blobUnavailable {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionUnavailable, "exact content for this version is unavailable")
		return
	}
	sessionID, turn := s.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.RestoreVersion(r.Context(), operationID.String(), p, project.SourceVersionRestoreRequest{
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
		s.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceVersionRestoreResponse{
		VersionID: result.VersionID, PreviousVersionID: result.PreviousVersionID,
		FileID: result.FileID, RootID: result.RootID,
		Path: result.Path, State: result.State, Sha256: result.SHA256, Changed: result.Changed,
	})
}

// gitSourcedVersion resolves and verifies repository-caused bytes.
func (s *Handler) gitSourcedVersion(
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
func (s *Handler) resolveRootRepoPosition(
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
func (s *Handler) HandleGetProjectSourceComparison(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	effectID := strings.TrimSpace(q.Get("effect_id"))
	versionID := strings.TrimSpace(q.Get("version_id"))
	fileID := strings.TrimSpace(q.Get("file_id"))
	blobOID := strings.TrimSpace(q.Get("blob_oid"))
	path := q.Get("path")
	modeCount := 0
	for _, value := range []string{effectID, versionID, fileID, blobOID, path} {
		if value != "" {
			modeCount++
		}
	}
	if modeCount != 1 {
		s.responses.InvalidQueryParam(w, "effect_id",
			"name exactly one comparison: effect_id, version_id, blob_oid, file_id, or root_id and path")
		return
	}
	if (q.Has("mark_user_edits") || q.Has("presentation_after_ordinal")) && fileID == "" {
		param := "presentation_after_ordinal"
		if q.Has("mark_user_edits") {
			param = "mark_user_edits"
		}
		s.responses.InvalidQueryParam(w, param, "applies only to a recorded scope comparison by file_id")
		return
	}
	if q.Has("reviewed_through_ordinal") {
		s.writeSourceReviewedComparison(w, r, p, fileID)
		return
	}
	if path != "" {
		s.writeCommitPathComparison(w, r, p)
		return
	}
	if effectID != "" {
		s.writeSourceEffectComparison(w, r, p, effectID)
		return
	}
	if versionID != "" {
		s.writeSourceVersionComparison(w, r, p, versionID)
		return
	}
	if blobOID != "" {
		s.writeSourceBlobComparison(w, r, p,
			strings.TrimSpace(q.Get("root_id")), blobOID,
			strings.TrimSpace(q.Get("before_blob_oid")))
		return
	}
	s.writeSourceScopeDiff(w, r, p, fileID)
}

func (s *Handler) writeSourceScopeDiff(w http.ResponseWriter, r *http.Request, p *project.Project, fileID string) {
	bas, err := sourceledger.ParseBaseline(r.URL.Query().Get("baseline"))
	if err != nil {
		s.responses.InvalidQuery(w, &httpio.QueryParameterError{Parameter: "baseline", Reason: "is not a source baseline"})
		return
	}
	if bas.Kind == sourceledger.BaselineCommit {
		s.responses.InvalidQueryParam(w, "baseline", "commit requires root_id and path")
		return
	}
	start, err := presentationComparisonStart(r, bas)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	mark, present, err := httpio.OptionalBoolQuery(r, "mark_user_edits")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	var markUserEdits *bool
	if present {
		markUserEdits = &mark
	}
	diff, err := s.loadScopeComparison(r.Context(), p, wire.ScopeComparisonSource{
		FileID: fileID, Baseline: r.URL.Query().Get("baseline"),
		MarkUserEdits: markUserEdits, PresentationAfterOrdinal: start,
	})
	if err != nil {
		s.writeComparisonError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func (s *Handler) writeSourceEffectComparison(w http.ResponseWriter, r *http.Request, p *project.Project, effectID string) {
	diff, err := s.SourceLedger.CompareEffect(r.Context(), p.ID, effectID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceEffectNotFound, "effect not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func (s *Handler) writeSourceVersionComparison(w http.ResponseWriter, r *http.Request, p *project.Project, versionID string) {
	diff, err := s.SourceLedger.CompareVersions(r.Context(), p.ID, versionID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionNotFound, "version not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func (s *Handler) MapSourceComparison(ctx context.Context, projectID string, d sourceledger.Comparison) wire.SourceComparison {
	out := mapUnscreenedSourceComparison(d)
	if d.InRange {
		screenCtx, _, err := secretview.ProjectContext(s.ManagedSecrets, ctx, projectID)
		if err == nil {
			for _, side := range []*wire.SourceComparisonSide{out.Before, out.After} {
				if side.Availability == "available" {
					side.SecretScreen = secretview.ScreenText(s.SecretSpans, screenCtx, side.Content)
				}
			}
		}
	}
	return out
}

func mapUnscreenedSourceComparison(d sourceledger.Comparison) wire.SourceComparison {
	out := wire.SourceComparison{
		PresentationAfterOrdinal: d.PresentationAfterOrdinal,
		InRange:                  d.InRange, EffectID: d.EffectID, FileID: d.FileID, Op: d.Op,
		LocationChanged: d.LocationChanged, UserEditsUnmarked: d.UserEditsUnmarked, Attribution: mapSourceAttribution(d.Attribution),
	}
	// Out-of-range comparisons omit both endpoints.
	if d.InRange {
		before, after := mapSourceComparisonSide(d.Before), mapSourceComparisonSide(d.After)
		out.Before, out.After = &before, &after
	}
	return out
}

func mapSourceComparisonSide(side sourceledger.ComparisonSide) wire.SourceComparisonSide {
	return wire.SourceComparisonSide{VersionID: side.VersionID, RootID: side.RootID,
		Path: side.Path, State: side.State, Sha256: side.SHA256, SizeBytes: side.SizeBytes,
		Availability: string(side.Availability), Reason: side.Reason, Content: side.Content}
}

// commitLens binds attached roots to known repository tops.
func (s *Handler) CommitLens(ctx context.Context, p *project.Project) sourceledger.CommitLens {
	mgr := s.Git.Manager()
	roots := project.RootRefsFrom(p)
	repos, err := s.Git.LoadOrderedRepos(ctx, p, roots, "", "")
	if err != nil {
		return sourceledger.CommitLens{}
	}
	topByRootID := make(map[string]string, len(roots))
	for _, repo := range repos {
		if !repo.Available || strings.TrimSpace(repo.Toplevel) == "" {
			continue
		}
		for _, id := range repo.RootIDs {
			topByRootID[id] = repo.Toplevel
		}
	}
	lens := sourceledger.CommitLens{}
	tops := make(map[string]string, len(roots))
	for _, root := range roots {
		top, inRepo := topByRootID[root.ID]
		if !inRepo {
			continue
		}
		abs, err := filepath.Abs(root.Path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		tops[abs] = top
		lens.Roots = append(lens.Roots, sourceledger.LensRoot{ID: root.ID, Abs: abs})
	}
	lens.Available = len(lens.Roots) > 0
	lens.Git = gitWorkingTreeAdapter{mgr: mgr, tops: tops}
	return lens
}

func (s *Handler) HandleGetProjectSourceAttribution(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	// Worktree-bound chats resolve attribution in their own workspace.
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	rootID := strings.TrimSpace(r.URL.Query().Get("root_id"))
	if rootID == "" {
		s.responses.InvalidQueryParam(w, "root_id", "is required")
		return
	}
	if path == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	res, err := s.SourceLedger.QueryAttribution(r.Context(), p.ID, p.BranchForRoot(rootID), rootID, path)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	intervals := make([]wire.SourceAttributionInterval, 0, len(res.Intervals))
	for _, iv := range res.Intervals {
		// The attribution view highlights only agent-authored lines.
		if iv.Origin != wire.SourceChangeOriginAgent {
			continue
		}
		row := wire.SourceAttributionInterval{
			StartLine:  iv.StartLine,
			EndLine:    iv.EndLine,
			Turn:       iv.Turn,
			RecordedAt: iv.TS,
		}
		if iv.SessionID != "" {
			row.SessionID = &iv.SessionID
		}
		if iv.ToolCallID != "" {
			row.ToolCallID = &iv.ToolCallID
		}
		intervals = append(intervals, row)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceAttributionResponse{
		HeadSha256: res.HeadSHA256,
		Intervals:  intervals,
	})
}

func (s *Handler) HandleGetWorkerChanges(w http.ResponseWriter, r *http.Request) {
	workerID := strings.TrimSpace(chi.URLParam(r, "id"))
	task, ok := s.Workers.Get(workerID)
	if !ok || task == nil {
		s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
		return
	}
	rows, err := s.SourceLedger.QueryJobChanges(r.Context(), task.ProjectID, workerID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	files := make([]wire.WorkerJobChangedFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, wire.WorkerJobChangedFile{RootID: row.RootID, Path: row.Path, Op: row.Op})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.WorkerJobChangesResponse{Files: files})
}
