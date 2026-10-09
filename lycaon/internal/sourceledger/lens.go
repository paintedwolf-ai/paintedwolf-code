package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"sort"
	"strings"
	"time"
)

var ErrBaselinePinNotFound = errors.New("baseline pin not found")

type BaselineKind string

const (
	BaselinePresentation BaselineKind = "presentation"
	BaselineTurn         BaselineKind = "turn"
	BaselineSession      BaselineKind = "session"
	BaselineCommit       BaselineKind = "commit"
	BaselinePin          BaselineKind = "pin"
)

type Baseline struct {
	RootBranches map[string]sourcebranch.ID
	Kind         BaselineKind
	SessionID    string
	Turn         int
	PinID        string
	// WithOutsideChanges widens a session baseline to the outside changes no
	// chat owns within the session's span.
	WithOutsideChanges bool
	// WithoutUserEdits drops the person's own writes, so a file only they
	// changed is not in the range.
	WithoutUserEdits bool
}

type Tip struct {
	State  api.SourceTipState
	SHA256 string
}

// Effect is one ordered Walk position with exact version endpoints.
type Effect struct {
	Contributors                                    []Contributor
	ID, ProjectID, OperationID, FileID              string
	BeforeVersionID, AfterVersionID                 string
	BranchID                                        sourcebranch.ID
	RootID, Path, FromRootID, FromPath              string
	EntryKind                                       string
	Op                                              api.SourceChangeOp
	Origin                                          api.SourceChangeOrigin
	Cause, ActorLabel                               string
	SessionID, JobID, ToolCallID, ToolName, BatchID string
	// Empty when the operation has no recorded ref movement.
	GitTransitionID string
	// Empty when no command observation window covered the operation.
	CommandWindowID string
	Turn            int
	CaptureQuality  string
	Ordinal         int64
	TS              time.Time
}

// WalkFile groups range effects for one logical file.
type WalkFile struct {
	FileID string
	RootID string
	Path   string
	LastTS time.Time
	Tip    Tip
	// Unknown when repository or captured-byte identity is unavailable.
	HeadMatch               api.SourceHeadMatch
	ChangedSincePresented   bool
	UnpresentedAgentEffects int64
	PresentationEffectID    string
	PresentationOrdinal     int64
	Effects                 []Effect
}

type WalkResult struct {
	// Baseline is the range this page answered.
	Baseline          Baseline
	Turns             []api.SourceWalkTurn
	Files             []WalkFile
	CommitAvailable   bool
	NextBeforeOrdinal int64
	// Movements selected by the baseline and references needed by page effects.
	GitChanges []GitTransition
	// Referenced observation windows, newest first.
	Commands []CommandWindow
}

type GitWorkingTree interface {
	// Missing blobs are omitted; false means the tree could not be read.
	TreeOIDs(ctx context.Context, rootAbs string, paths []string) (map[string]string, bool)
}

type LensRoot struct{ ID, Abs string }

type CommitLens struct {
	Git       GitWorkingTree
	Roots     []LensRoot
	Available bool
}

// QueryWalk pages Git movements and groups file effects by logical file.
func (s *Walk) QueryWalk(
	ctx context.Context,
	projectID string,
	baseline Baseline,
	limit int,
	beforeOrdinal int64,
	lens CommitLens,
) (WalkResult, error) {
	if s == nil {
		return WalkResult{}, fmt.Errorf("ledger not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeOrdinal < 0 {
		beforeOrdinal = 0
	}
	// Turn zero stamps edits made while no turn was active; it is not a turn.
	if baseline.Kind == BaselineTurn && baseline.Turn < 1 {
		return WalkResult{Baseline: baseline, CommitAvailable: lens.Available}, nil
	}
	effects, movements, next, err := s.walkPage(ctx, projectID, baseline, limit, beforeOrdinal)
	if err != nil {
		return WalkResult{}, err
	}
	if err := s.hydrateEffectAuthors(ctx, projectID, effects); err != nil {
		return WalkResult{}, err
	}
	out := WalkResult{Baseline: baseline, CommitAvailable: lens.Available, GitChanges: movements, NextBeforeOrdinal: next}
	// The cursor above stays on the unfiltered page, so paging still advances.
	if baseline.WithoutUserEdits {
		effects = slices.DeleteFunc(effects, func(e Effect) bool {
			return e.onlyUserContributions()
		})
	}
	states, err := s.walkFileStates(ctx, projectID, baseline.RootBranches, effects)
	if err != nil {
		return out, err
	}
	byFile := make(map[string]int, len(effects))
	for _, effect := range effects {
		index, ok := byFile[effect.FileID]
		if !ok {
			index = len(out.Files)
			byFile[effect.FileID] = index
			group := WalkFile{
				FileID: effect.FileID, RootID: effect.RootID, Path: effect.Path,
				LastTS: effect.TS, HeadMatch: api.SourceHeadMatchUnknown,
			}
			if state, found := states[effect.FileID]; found {
				if state.rootID != "" {
					group.RootID, group.Path = state.rootID, state.path
				}
				switch state.state {
				case "absent":
					group.Tip.State = api.SourceTipStateAbsent
				case "content":
					group.Tip = Tip{State: api.SourceTipStateContent, SHA256: state.contentSHA256}
				}
				group.PresentationEffectID = state.presentationEffectID
				group.PresentationOrdinal = state.presentationOrdinal
				group.ChangedSincePresented = state.presentationOrdinal > state.throughOrdinal
				group.UnpresentedAgentEffects = state.unpresentedAgentEffects
			}
			out.Files = append(out.Files, group)
		}
		out.Files[index].Effects = append(out.Files[index].Effects, effect)
	}
	out.Commands, err = s.walkCommandWindows(ctx, effects)
	if err != nil {
		return out, err
	}
	out.Turns, err = s.walkTurns(ctx, projectID, effects, out.GitChanges)
	if err != nil {
		return out, err
	}
	s.resolveHeadMatches(ctx, &out, lens)
	return out, nil
}

// walkCommandWindows resolves the windows this page's effects name.
func (s *Walk) walkCommandWindows(ctx context.Context, effects []Effect) ([]CommandWindow, error) {
	ids := make([]string, 0, 4)
	for _, effect := range effects {
		if effect.CommandWindowID != "" {
			ids = append(ids, effect.CommandWindowID)
		}
	}
	byID, err := s.commands.commandWindowsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]CommandWindow, 0, len(byID))
	for _, window := range byID {
		out = append(out, window)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ordinal > out[j].Ordinal })
	return out, nil
}

// Failed object lookups leave HeadMatch unknown.
func (s *Walk) resolveHeadMatches(ctx context.Context, out *WalkResult, lens CommitLens) {
	if !lens.Available || lens.Git == nil || len(out.Files) == 0 {
		return
	}
	oidsBySHA := s.blobOIDsForTips(ctx, out.Files)
	pathsByRoot := make(map[string][]string, len(lens.Roots))
	for _, file := range out.Files {
		switch file.Tip.State {
		case api.SourceTipStateContent:
			// Uncaptured bytes cannot be compared with repository objects.
			if _, ok := oidsBySHA[file.Tip.SHA256]; !ok {
				continue
			}
		case api.SourceTipStateAbsent:
		default:
			continue
		}
		pathsByRoot[file.RootID] = append(pathsByRoot[file.RootID], file.Path)
	}
	headByRoot := make(map[string]map[string]string, len(pathsByRoot))
	answered := make(map[string]bool, len(pathsByRoot))
	for _, root := range lens.Roots {
		paths := pathsByRoot[root.ID]
		if len(paths) == 0 {
			continue
		}
		oids, ok := lens.git.TreeOIDs(ctx, root.Abs, paths)
		headByRoot[root.ID], answered[root.ID] = oids, ok
	}
	for i := range out.Files {
		file := &out.Files[i]
		if !answered[file.RootID] {
			continue
		}
		if file.Tip.State != api.SourceTipStateContent && file.Tip.State != api.SourceTipStateAbsent {
			continue
		}
		headOID, inHead := headByRoot[file.RootID][file.Path]
		switch {
		case file.Tip.State == api.SourceTipStateAbsent && !inHead:
			file.HeadMatch = api.SourceHeadMatchSame
		case file.Tip.State == api.SourceTipStateAbsent:
			file.HeadMatch = api.SourceHeadMatchDiffers
		case !inHead:
			file.HeadMatch = api.SourceHeadMatchAbsent
		default:
			tip, ok := oidsBySHA[file.Tip.SHA256]
			if !ok {
				continue
			}
			if headOID == tip.SHA1 || headOID == tip.SHA256 {
				file.HeadMatch = api.SourceHeadMatchSame
			} else {
				file.HeadMatch = api.SourceHeadMatchDiffers
			}
		}
	}
}

// Digests without stored bytes have no derived object ids.
func (s *Walk) blobOIDsForTips(ctx context.Context, files []WalkFile) map[string]sourceblob.GitOIDs {
	shas := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.Tip.State != api.SourceTipStateContent || file.Tip.SHA256 == "" {
			continue
		}
		if _, ok := seen[file.Tip.SHA256]; ok {
			continue
		}
		seen[file.Tip.SHA256] = struct{}{}
		shas = append(shas, file.Tip.SHA256)
	}
	out := make(map[string]sourceblob.GitOIDs, len(shas))
	if len(shas) == 0 {
		return out
	}
	raw, err := json.Marshal(shas)
	if err != nil {
		return out
	}
	rows, err := s.queries.ListSourceBlobGitOIDs(ctx, string(raw))
	if err != nil {
		return out
	}
	for _, row := range rows {
		out[row.Sha256] = sourceblob.GitOIDs{SHA1: row.GitOidSha1, SHA256: row.GitOidSha256}
	}
	return out
}

type walkFileState struct {
	rootID, path, state, contentSHA256  string
	presentationEffectID                string
	presentationOrdinal, throughOrdinal int64
	unpresentedAgentEffects             int64
}

// walkFileStates reads each file's head on the branch its root reads under
// the request, so trunk and a chat's worktree never answer for each other.
func (s *Walk) walkFileStates(
	ctx context.Context,
	projectID string,
	rootBranches map[string]sourcebranch.ID,
	effects []Effect,
) (map[string]walkFileState, error) {
	ids := make([]string, 0, len(effects))
	seen := make(map[string]struct{}, len(effects))
	for _, effect := range effects {
		if _, ok := seen[effect.FileID]; ok {
			continue
		}
		seen[effect.FileID] = struct{}{}
		ids = append(ids, effect.FileID)
	}
	out := make(map[string]walkFileState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	roots, err := encodeRootBranches(rootBranches)
	if err != nil {
		return nil, err
	}
	// A root outside the request's mapping reads trunk.
	rows, err := s.sqlDB.QueryContext(ctx, `
		WITH requested(file_id) AS (
			SELECT CAST(value AS TEXT) FROM json_each(?)
		), scoped_roots(root_id, branch_id) AS (
			SELECT CAST(key AS TEXT), CAST(value AS TEXT)
			FROM json_each(CASE WHEN ? = '' THEN '{}' ELSE ? END)
		), latest(file_id, ordinal) AS (
			SELECT e.file_id, MAX(e.ordinal)
			FROM source_effects e
			JOIN source_operations o ON o.id = e.operation_id
			JOIN requested r ON r.file_id = e.file_id
			WHERE e.project_id = ? AND e.walk_visible = 1
			  AND o.branch_id = COALESCE((SELECT sr.branch_id FROM scoped_roots sr WHERE sr.root_id = e.root_id), '')
			GROUP BY e.file_id
		), pending(file_id, effect_count) AS (
			SELECT p.file_id, COUNT(*)
			FROM source_agent_presentations p
			JOIN requested r ON r.file_id = p.file_id
			WHERE p.project_id = ?
			GROUP BY p.file_id
		)
		SELECT r.file_id, COALESCE(h.root_id, ''), COALESCE(h.path, ''),
		       COALESCE(h.state, ''), COALESCE(h.content_sha256, ''),
		       COALESCE(e.id, ''), COALESCE(latest.ordinal, 0),
		       COALESCE(w.through_ordinal, 0), COALESCE(p.effect_count, 0)
		FROM requested r
		LEFT JOIN source_branch_heads h
		  ON h.project_id = ? AND h.file_id = r.file_id
		 AND h.branch_id = COALESCE((SELECT sr.branch_id FROM scoped_roots sr WHERE sr.root_id = h.root_id), '')
		LEFT JOIN latest ON latest.file_id = r.file_id
		LEFT JOIN source_effects e
		  ON e.project_id = ? AND e.file_id = latest.file_id AND e.ordinal = latest.ordinal
		LEFT JOIN source_presentation_watermarks w
		  ON w.project_id = ? AND w.file_id = r.file_id
		LEFT JOIN pending p ON p.file_id = r.file_id
	`, string(raw), roots, roots, projectID, projectID, projectID, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var fileID string
		var state walkFileState
		if err := rows.Scan(&fileID, &state.rootID, &state.path, &state.state,
			&state.contentSHA256, &state.presentationEffectID, &state.presentationOrdinal,
			&state.throughOrdinal, &state.unpresentedAgentEffects); err != nil {
			return nil, err
		}
		out[fileID] = state
	}
	return out, rows.Err()
}

func (s *Walk) queryEffects(
	ctx context.Context,
	projectID string,
	baseline Baseline,
	limit int,
	beforeOrdinal int64,
) ([]Effect, error) {
	rootBranches, err := encodeRootBranches(baseline.RootBranches)
	if err != nil {
		return nil, err
	}
	switch baseline.Kind {
	case BaselineCommit:
		return nil, fmt.Errorf("commit comparisons are path queries, not recorded history")
	case BaselinePresentation:
		rows, err := s.queries.ListSourceEffectsForPresentation(ctx, db.ListSourceEffectsForPresentationParams{
			ProjectID: projectID, RootBranches: rootBranches, BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromPresentation(rows), err
	case BaselineTurn:
		rows, err := s.queries.ListSourceEffectsForTurn(ctx, db.ListSourceEffectsForTurnParams{
			ProjectID: projectID, RootBranches: rootBranches, SessionID: baseline.SessionID, Turn: int64(baseline.Turn),
			BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromTurn(rows), err
	case BaselineSession:
		if baseline.WithOutsideChanges {
			rows, err := s.queries.ListSourceEffectsForSessionWithOutside(ctx, db.ListSourceEffectsForSessionWithOutsideParams{
				ProjectID: projectID, RootBranches: rootBranches, SessionID: baseline.SessionID,
				BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
			})
			return effectsFromSessionWithOutside(rows), err
		}
		rows, err := s.queries.ListSourceEffectsForSession(ctx, db.ListSourceEffectsForSessionParams{
			ProjectID: projectID, RootBranches: rootBranches, SessionID: baseline.SessionID,
			BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromSession(rows), err
	case BaselinePin:
		ordinal, err := s.resolvePinOrdinal(ctx, projectID, baseline)
		if err != nil {
			return nil, err
		}
		rows, err := s.queries.ListSourceEffectsAfterOrdinal(ctx, db.ListSourceEffectsAfterOrdinalParams{
			ProjectID: projectID, RootBranches: rootBranches, Ordinal: ordinal,
			BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromAfterOrdinal(rows), err
	default:
		rows, err := s.queries.ListSourceEffectsForProject(ctx, db.ListSourceEffectsForProjectParams{
			ProjectID: projectID, RootBranches: rootBranches, BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromProject(rows), err
	}
}

// json_each treats JSON null as one row, so empty sets encode as [].
func jsonArray[T any](values []T) (string, error) {
	if len(values) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

type effectFields struct {
	id, projectID, operationID, fileID, beforeVersionID, afterVersionID string
	rootID, path, fromRootID, fromPath, op, entryKind, createdTS        string
	branchID, origin, cause, actorLabel                                 string
	sessionID, jobID, toolCallID, toolName, batchID, captureQuality     string
	gitTransitionID, commandWindowID                                    string
	ordinal, turn                                                       int64
}

func effectFromFields(row effectFields) Effect {
	ts, _ := time.Parse(time.RFC3339Nano, row.createdTS)
	return Effect{
		ID: row.id, ProjectID: row.projectID, OperationID: row.operationID,
		FileID: row.fileID, BeforeVersionID: row.beforeVersionID,
		AfterVersionID: row.afterVersionID, RootID: row.rootID, Path: row.path,
		FromRootID: row.fromRootID, FromPath: row.fromPath,
		Op: api.SourceChangeOp(row.op), EntryKind: row.entryKind,
		BranchID: sourcebranch.ID(row.branchID),
		Origin:   api.SourceChangeOrigin(row.origin), Cause: row.cause, ActorLabel: row.actorLabel,
		SessionID: row.sessionID, JobID: row.jobID, Turn: int(row.turn),
		ToolCallID: row.toolCallID, ToolName: row.toolName, BatchID: row.batchID,
		GitTransitionID: row.gitTransitionID, CommandWindowID: row.commandWindowID,
		CaptureQuality: row.captureQuality, Ordinal: row.ordinal, TS: ts,
	}
}

func effectFieldsOf(
	id, projectID, operationID, fileID, beforeVersionID, afterVersionID,
	rootID, path, fromRootID, fromPath, op, entryKind string,
	ordinal int64,
	createdTS, branchID, origin, cause, actorLabel,
	sessionID, jobID string,
	turn int64,
	toolCallID, toolName, batchID, captureQuality, gitTransitionID, commandWindowID string,
) effectFields {
	return effectFields{id: id, projectID: projectID, operationID: operationID,
		fileID: fileID, beforeVersionID: beforeVersionID, afterVersionID: afterVersionID,
		rootID: rootID, path: path, fromRootID: fromRootID, fromPath: fromPath,
		op: op, entryKind: entryKind, ordinal: ordinal, createdTS: createdTS,
		branchID: branchID, origin: origin,
		cause: cause, actorLabel: actorLabel, sessionID: sessionID, jobID: jobID,
		turn: turn, toolCallID: toolCallID, toolName: toolName, batchID: batchID,
		captureQuality: captureQuality, gitTransitionID: gitTransitionID,
		commandWindowID: commandWindowID}
}

func effectOfProject(r db.ListSourceEffectsForProjectRow) Effect {
	return effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID,
		r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath,
		r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID,
		r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn,
		r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID))
}

func effectsFromProject(rows []db.ListSourceEffectsForProjectRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, row := range rows {
		out = append(out, effectOfProject(row))
	}
	return out
}
func effectsFromPresentation(rows []db.ListSourceEffectsForPresentationRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromTurn(rows []db.ListSourceEffectsForTurnRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromSession(rows []db.ListSourceEffectsForSessionRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromSessionWithOutside(rows []db.ListSourceEffectsForSessionWithOutsideRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromAfterOrdinal(rows []db.ListSourceEffectsAfterOrdinalRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}

type JobChangedFile struct {
	FileID, RootID, Path string
	Op                   api.SourceChangeOp
	LastTS               time.Time
}

const MaxJobChangedFiles = 2000

func (s *Walk) QueryJobChanges(ctx context.Context, projectID, jobID string) ([]JobChangedFile, error) {
	rows, err := s.queries.ListJobChangedFiles(ctx, db.ListJobChangedFilesParams{
		ProjectID: strings.TrimSpace(projectID), JobID: strings.TrimSpace(jobID), Limit: MaxJobChangedFiles,
	})
	if err != nil {
		return nil, err
	}
	out := make([]JobChangedFile, 0, len(rows))
	for _, row := range rows {
		ts, _ := time.Parse(time.RFC3339Nano, asString(row.LastTs))
		out = append(out, JobChangedFile{FileID: row.FileID, RootID: row.RootID, Path: row.Path, Op: api.SourceChangeOp(row.Op), LastTS: ts})
	}
	return out, nil
}

func (s *Walk) JobPathFirstWriteOrder(ctx context.Context, projectID, jobID string) ([]string, error) {
	rows, err := s.queries.ListJobChangedFilesByFirstWrite(ctx, db.ListJobChangedFilesByFirstWriteParams{ProjectID: projectID, JobID: jobID, Limit: MaxJobChangedFiles})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if p := strings.TrimSpace(row.Path); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Walk) JobVersionForPath(
	ctx context.Context,
	projectID, jobID, rootID, path string,
) (string, string, error) {
	row, err := s.queries.LatestJobSourceVersionForPath(ctx, db.LatestJobSourceVersionForPathParams{
		ProjectID: projectID, JobID: jobID, RootID: rootID, Path: path,
	})
	if err != nil {
		return "", "", err
	}
	return row.FileID, row.AfterVersionID, nil
}

const MaxSessionAuthoredFiles = 2000

func (s *Walk) SessionAuthoredPaths(ctx context.Context, projectID, sessionID, rootID string) ([]string, error) {
	rows, err := s.queries.ListSessionAuthoredFiles(ctx, db.ListSessionAuthoredFilesParams{ProjectID: projectID, SessionID: sessionID, RootID: rootID, Limit: MaxSessionAuthoredFiles})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows)*2)
	seen := make(map[string]struct{}, len(rows)*2)
	for _, row := range rows {
		for _, candidate := range []string{row.FromPath, row.Path} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				if _, ok := seen[candidate]; !ok {
					seen[candidate] = struct{}{}
					out = append(out, candidate)
				}
			}
		}
	}
	return out, nil
}

func (s *Walk) resolvePinOrdinal(ctx context.Context, projectID string, baseline Baseline) (int64, error) {
	if baseline.Kind != BaselinePin {
		return 0, nil
	}
	checkpoint, err := s.queries.GetSourceCheckpoint(ctx, baseline.PinID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (checkpoint.ProjectID != projectID || checkpoint.Kind != CheckpointNamed) {
		return 0, ErrBaselinePinNotFound
	}
	if err != nil {
		return 0, err
	}
	return checkpoint.CreatedOrdinal, nil
}

// String spells the baseline in the grammar ParseBaseline reads.
func (b Baseline) String() string {
	switch b.Kind {
	case BaselineTurn:
		return fmt.Sprintf("turn:%s,%d", b.SessionID, b.Turn)
	case BaselineSession:
		return "session:" + b.SessionID
	case BaselinePin:
		return "pin:" + b.PinID
	case BaselineCommit:
		return string(BaselineCommit)
	default:
		return string(BaselinePresentation)
	}
}

// NamesChat reports whether the baseline reads one chat's history.
func (b Baseline) NamesChat() bool {
	return b.Kind == BaselineTurn || b.Kind == BaselineSession
}

// ParseCurrentTurn recognizes `turn:{session_id}`, which names that chat's
// current user turn. The reader resolves the ordinal before querying.
func ParseCurrentTurn(raw string) (Baseline, bool) {
	value, ok := strings.CutPrefix(strings.TrimSpace(raw), "turn:")
	if !ok || strings.Contains(value, ",") || strings.TrimSpace(value) == "" {
		return Baseline{}, false
	}
	return Baseline{Kind: BaselineTurn, SessionID: strings.TrimSpace(value)}, true
}

func ParseBaseline(raw string) (Baseline, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "presentation" {
		return Baseline{Kind: BaselinePresentation}, nil
	}
	if raw == "commit" {
		return Baseline{Kind: BaselineCommit}, nil
	}
	if value, ok := strings.CutPrefix(raw, "session:"); ok && strings.TrimSpace(value) != "" {
		return Baseline{Kind: BaselineSession, SessionID: strings.TrimSpace(value)}, nil
	}
	if value, ok := strings.CutPrefix(raw, "pin:"); ok && strings.TrimSpace(value) != "" {
		return Baseline{Kind: BaselinePin, PinID: strings.TrimSpace(value)}, nil
	}
	if value, ok := strings.CutPrefix(raw, "turn:"); ok {
		parts := strings.SplitN(value, ",", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return Baseline{}, fmt.Errorf("turn baseline wants session,turn")
		}
		var turn int
		if _, err := fmt.Sscanf(parts[1], "%d", &turn); err != nil || turn < 1 {
			return Baseline{}, fmt.Errorf("turn baseline wants a positive turn")
		}
		return Baseline{Kind: BaselineTurn, SessionID: strings.TrimSpace(parts[0]), Turn: turn}, nil
	}
	return Baseline{}, fmt.Errorf("unknown baseline %q", raw)
}

// Walk projects causal source activity under an explicit history lens.
type Walk struct {
	queries  *db.Queries
	sqlDB    db.Handle
	commands walkCommandsPort
	git      walkGitPort
}

type walkCommandsPort interface {
	commandWindowsFor(ctx context.Context, ids []string) (map[string]CommandWindow, error)
}

type walkGitPort interface {
	GitTransitionsByIDs(ctx context.Context, ids []string) (map[string]GitTransition, error)
}
