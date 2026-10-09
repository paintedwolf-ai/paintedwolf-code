package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *History) HandleGetProjectSourceWalkSummary(w http.ResponseWriter, r *http.Request) {
	project, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	project, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, project)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		s.responses.InvalidQueryParam(w, "session_id", "is required")
		return
	}
	ids := strings.Split(r.URL.Query().Get("message_ids"), ",")
	if len(ids) > 100 || strings.TrimSpace(ids[0]) == "" {
		s.responses.InvalidQueryParam(w, "message_ids", "must list 1 to 100 message ids")
		return
	}
	rows, err := s.SourceLedger.WalkSummary(r.Context(), project.ID, sessionID, ids)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceWalkSummary{Turns: rows})
}

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

func (s *History) HandleListProjectSourceWalk(w http.ResponseWriter, r *http.Request) {
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
		s.Review.writeCommitReview(w, r, p, scope, query.page)
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
		r.Context(), p.ID, bas, query.page.Limit, beforeOrdinal, s.Comparisons.CommitLens(r.Context(), p),
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

func (s *History) parseSourceWalkQuery(w http.ResponseWriter, r *http.Request) (sourceWalkQuery, bool) {
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
func (s *History) scopeProjectForWalk(w http.ResponseWriter, r *http.Request, p *project.Project, bas sourceledger.Baseline) (*project.Project, string, bool) {
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
