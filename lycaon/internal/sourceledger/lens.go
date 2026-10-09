package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
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
		oids, ok := lens.Git.TreeOIDs(ctx, root.Abs, paths)
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
