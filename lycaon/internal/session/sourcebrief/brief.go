package sourcebrief

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/messageview"
	"log/slog"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	PutTurnSourceBrief(context.Context, string, string, evidence.SourceChangeMint) error
	TurnSourceBriefs(context.Context, string) (map[string]string, error)
	ReadSourceTurn(context.Context, string, int) (messageview.SourceTurn, error)
	LoadLedger(context.Context, string) (evidence.Ledger, error)
}

type Roots interface {
	Roots(context.Context, *api.Session) ([]projectroot.RootRef, error)
}

type Service struct {
	store       Store
	roots       Roots
	history     tools.SourceHistory
	checkpoints Checkpoints
}

func New(store Store, roots Roots) *Service { return &Service{store: store, roots: roots} }
func (m *Service) SetSources(history tools.SourceHistory, checkpoints Checkpoints) {
	m.history = history
	m.checkpoints = checkpoints
}

const (
	// sourceChangeBriefEffectCap bounds the effects read per class for one
	// window. An overflowing class renders as truncated, never as a complete count.
	sourceChangeBriefEffectCap = 400
	// sourceChangeBriefFileCap bounds the itemized rows across both classes.
	sourceChangeBriefFileCap = 20
	// sourceChangeBriefGitCap bounds itemized ref movements per window.
	sourceChangeBriefGitCap = 10
)

type Checkpoints interface {
	TurnCheckpoint(context.Context, string, string, int) (sourceledger.Checkpoint, bool, error)
}

// Record stores the source-change brief a coordinator turn
// opens with; assembly restates it unchanged on every later call.
func (m *Service) Record(ctx context.Context, sess *api.Session, openingMessageID string) {
	if m == nil || sess == nil || strings.TrimSpace(openingMessageID) == "" || !surface.IsCoordinatorSession(sess) || sess.IsWorkerChild() {
		return
	}
	brief := m.Build(ctx, sess)
	if brief.Empty() {
		return
	}
	mint := func(firstOrdinal int) (string, []evidence.Record, error) {
		cited, records := citableBrief(brief, firstOrdinal)
		raw, err := json.Marshal(cited)
		return string(raw), records, err
	}
	if err := m.store.PutTurnSourceBrief(ctx, sess.ID, openingMessageID, mint); err != nil {
		slog.WarnContext(ctx, "source change brief not recorded", "session_id", sess.ID, "err", err)
	}
}

// Recorded returns the session's recorded briefs keyed by the message
// that opened each turn.
func (m *Service) Recorded(ctx context.Context, sess *api.Session) map[string]inject.SourceChangeBrief {
	if m == nil || m.store == nil || sess == nil {
		return nil
	}
	recorded, err := m.store.TurnSourceBriefs(ctx, sess.ID)
	if err != nil || len(recorded) == 0 {
		return nil
	}
	out := make(map[string]inject.SourceChangeBrief, len(recorded))
	for openingID, raw := range recorded {
		var brief inject.SourceChangeBrief
		if json.Unmarshal([]byte(raw), &brief) == nil && !brief.Empty() {
			out[openingID] = brief
		}
	}
	return out
}

// Build fixes the foreign-change window at turn start.
// Missing boundaries leave the baseline unknown.
func (m *Service) Build(ctx context.Context, sess *api.Session) inject.SourceChangeBrief {
	if m == nil || sess == nil || sess.ProjectID == "" {
		return inject.SourceChangeBrief{}
	}
	reader := m.history
	if reader.Files == nil || reader.Git == nil || reader.Authorship == nil || m.checkpoints == nil || m.store == nil {
		return inject.SourceChangeBrief{}
	}
	turnSource, err := m.store.ReadSourceTurn(ctx, sess.ID, 0)
	turn := turnSource.Number
	if err != nil || turn < 2 {
		return inject.SourceChangeBrief{}
	}
	current, foundCurrent, err := m.checkpoints.TurnCheckpoint(ctx, sess.ProjectID, sess.ID, turn)
	if err != nil || !foundCurrent {
		return inject.SourceChangeBrief{}
	}
	previous, foundPrevious, err := m.checkpoints.TurnCheckpoint(ctx, sess.ProjectID, sess.ID, turn-1)
	if err != nil || !foundPrevious {
		return inject.SourceChangeBrief{}
	}
	// Each class has its own cap, so a build's output cannot fill the page a
	// person's edit is on.
	readClass := func(duringCommand bool) ([]sourceledger.Effect, bool, error) {
		effects, err := reader.Files.EffectsForBriefClass(
			ctx, sess.ProjectID, sess.ID, duringCommand, previous.CreatedOrdinal, current.CreatedOrdinal, sourceChangeBriefEffectCap+1,
		)
		if len(effects) > sourceChangeBriefEffectCap {
			return effects[:sourceChangeBriefEffectCap], true, err
		}
		return effects, false, err
	}
	foreign, foreignOverflow, err := readClass(false)
	if err != nil {
		return inject.SourceChangeBrief{}
	}
	own, ownOverflow, err := readClass(true)
	if err != nil {
		return inject.SourceChangeBrief{}
	}
	// Ref movements are part of the same window: a bare commit changes what
	// "since last commit" means even when no file byte moved.
	transitions, err := reader.Git.GitTransitionsBetween(
		ctx, sess.ProjectID, previous.CreatedOrdinal, current.CreatedOrdinal, sourceChangeBriefGitCap,
	)
	if err != nil {
		transitions = nil
	}
	if len(foreign) == 0 && len(own) == 0 && len(transitions) == 0 {
		return inject.SourceChangeBrief{}
	}
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return inject.SourceChangeBrief{}
	}
	return assembleSourceChangeBrief(sourceChangeBriefInputs{
		Effects:      slices.Concat(foreign, own),
		Transitions:  transitions,
		Truncated:    foreignOverflow,
		OwnTruncated: ownOverflow,
		SessionID:    sess.ID,
		Roots:        roots,
		ActiveRootID: sess.WorkspaceRootID,
		Touched:      m.sessionTouchedPaths(ctx, sess, reader, roots),
	})
}

type sourceChangeBriefInputs struct {
	Effects      []sourceledger.Effect
	Transitions  []sourceledger.GitTransition
	Truncated    bool
	OwnTruncated bool
	SessionID    string
	Roots        []projectroot.RootRef
	ActiveRootID string
	Touched      map[string]bool
}

// fileGroups collects one class of effects per file. Effects arrive newest
// first, so a group's first effect is its newest and order is newest-file first.
type fileGroups struct {
	byFile map[string]*fileGroup
	order  []string
}

type fileGroup struct {
	newest  sourceledger.Effect
	effects int
}

func newFileGroups() *fileGroups {
	return &fileGroups{byFile: make(map[string]*fileGroup)}
}

func (g *fileGroups) add(effect sourceledger.Effect) {
	if group, seen := g.byFile[effect.FileID]; seen {
		group.effects++
		return
	}
	g.byFile[effect.FileID] = &fileGroup{newest: effect, effects: 1}
	g.order = append(g.order, effect.FileID)
}

// assembleSourceChangeBrief itemizes changed files up to the row cap: foreign
// changes first, working set leading, then files only the session's own command
// windows touched, so a build's output cannot displace a person's edit. Files
// that did not fit are counted. Observed ref movements precede their file effects.
func assembleSourceChangeBrief(in sourceChangeBriefInputs) inject.SourceChangeBrief {
	brief := inject.SourceChangeBrief{Truncated: in.Truncated, OwnTruncated: in.OwnTruncated}
	for i := len(in.Transitions) - 1; i >= 0; i-- {
		brief.Git = append(brief.Git, sourceGitLine(in.Roots, in.Transitions[i]))
	}
	foreign, own := newFileGroups(), newFileGroups()
	for _, effect := range in.Effects {
		if effect.BranchID.IsWorker() {
			continue
		}
		if effect.ActorClassFor(in.SessionID) == sourceledger.ActorYou {
			continue
		}
		if effect.DuringCommandOf(in.SessionID) {
			own.add(effect)
			continue
		}
		foreign.add(effect)
	}
	itemize := func(groups *fileGroups, workingSet, duringCommand bool) {
		for _, fileID := range groups.order {
			group := groups.byFile[fileID]
			// A file another actor also changed is that actor's row.
			if _, shared := foreign.byFile[fileID]; duringCommand && shared {
				continue
			}
			display := projectroot.DisplayPath(in.Roots, in.ActiveRootID, group.newest.RootID, group.newest.Path)
			if (in.Touched[group.newest.Path] || in.Touched[display]) != workingSet {
				continue
			}
			if len(brief.Files) >= sourceChangeBriefFileCap {
				brief.OtherFiles++
				brief.OtherEffects += group.effects
				brief.OtherWorkingSet = brief.OtherWorkingSet || workingSet
				continue
			}
			brief.Files = append(brief.Files, inject.SourceChangeFile{
				Path:              display,
				Actor:             string(group.newest.ActorClassFor(in.SessionID)),
				Detail:            group.newest.ActorDisplay(in.SessionID),
				Op:                string(group.newest.Op),
				At:                group.newest.TS.UTC().Format("15:04") + " UTC",
				Effects:           group.effects,
				WorkingSet:        workingSet,
				DuringYourCommand: duringCommand,
			})
		}
	}
	itemize(foreign, true, false)
	itemize(foreign, false, false)
	itemize(own, true, true)
	itemize(own, false, true)
	return brief
}

// sessionTouchedPaths joins observed and authored paths. Failed lookups remain unclassified.
func (m *Service) sessionTouchedPaths(
	ctx context.Context,
	sess *api.Session,
	reader tools.SourceHistory,
	roots []projectroot.RootRef,
) map[string]bool {
	touched := make(map[string]bool)
	if ev, err := m.store.LoadLedger(ctx, sess.ID); err == nil {
		for path := range ev.ByPath {
			if normalized := evidence.NormalizeLedgerPath(path); normalized != "" {
				touched[normalized] = true
			}
		}
	}
	for _, root := range roots {
		authored, err := reader.Authorship.SessionAuthoredPaths(ctx, sess.ProjectID, sess.ID, root.ID)
		if err != nil {
			continue
		}
		for _, path := range authored {
			if path = strings.TrimSpace(path); path != "" {
				touched[path] = true
				touched[projectroot.DisplayPath(roots, sess.WorkspaceRootID, root.ID, path)] = true
			}
		}
	}
	return touched
}

// sourceGitLine renders one transition as brief facts: abbreviated commits,
// short branch names, and git's own subject as the detail.
func sourceGitLine(roots []projectroot.RootRef, t sourceledger.GitTransition) inject.SourceGitLine {
	rootLabel := ""
	for _, root := range roots {
		if root.ID == t.RootID && root.Label != "" {
			rootLabel = "@" + root.Label
		}
	}
	return inject.SourceGitLine{
		Root: rootLabel, Kind: t.Kind,
		FromRef: t.FromRef, ToRef: t.ToRef,
		FromCommit: shortCommit(t.FromCommit), ToCommit: shortCommit(t.ToCommit),
		Detail: t.Detail,
		At:     t.ObservedTS.UTC().Format("15:04") + " UTC",
	}
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
