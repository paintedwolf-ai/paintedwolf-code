package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// sourceChangeBriefEffectCap bounds the effects pulled for one window. An
	// overflowing window renders as truncated, never as a complete count.
	sourceChangeBriefEffectCap = 400
	// sourceChangeBriefFileCap bounds the itemized working-set rows.
	sourceChangeBriefFileCap = 20
	// sourceChangeBriefGitCap bounds itemized ref movements per window.
	sourceChangeBriefGitCap = 10
)

// recordTurnSourceBrief stores the source-change brief a coordinator turn
// opens with; assembly restates it unchanged on every later call.
func (m *Manager) recordTurnSourceBrief(ctx context.Context, sess *api.Session, openingMessageID string) {
	if m == nil || sess == nil || strings.TrimSpace(openingMessageID) == "" || !surface.IsCoordinatorSession(sess) || sess.IsWorkerChild() {
		return
	}
	brief := m.buildSourceChangeBrief(ctx, sess)
	if brief.Empty() {
		return
	}
	raw, err := json.Marshal(brief)
	if err != nil {
		return
	}
	if err := m.store.PutTurnSourceBrief(ctx, sess.ID, openingMessageID, string(raw)); err != nil {
		slog.WarnContext(ctx, "source change brief not recorded", "session_id", sess.ID, "err", err)
	}
}

// turnSourceBriefs returns the session's recorded briefs keyed by the message
// that opened each turn.
func (m *Manager) turnSourceBriefs(ctx context.Context, sess *api.Session) map[string]inject.SourceChangeBrief {
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

// buildSourceChangeBrief fixes the foreign-change window at turn start.
// Missing boundaries leave the baseline unknown.
func (m *Manager) buildSourceChangeBrief(ctx context.Context, sess *api.Session) inject.SourceChangeBrief {
	if m == nil || sess == nil || sess.ProjectID == "" {
		return inject.SourceChangeBrief{}
	}
	reader := m.sourceHistory
	if reader.Files == nil || reader.Git == nil || reader.Authorship == nil || m.sourceCheckpoints == nil || m.store == nil {
		return inject.SourceChangeBrief{}
	}
	turn, err := m.store.UserTurnOrdinal(ctx, sess.ID)
	if err != nil || turn < 2 {
		return inject.SourceChangeBrief{}
	}
	current, foundCurrent, err := m.sourceCheckpoints.TurnCheckpoint(ctx, sess.ProjectID, sess.ID, turn)
	if err != nil || !foundCurrent {
		return inject.SourceChangeBrief{}
	}
	previous, foundPrevious, err := m.sourceCheckpoints.TurnCheckpoint(ctx, sess.ProjectID, sess.ID, turn-1)
	if err != nil || !foundPrevious {
		return inject.SourceChangeBrief{}
	}
	effects, err := reader.Files.EffectsBetween(
		ctx, sess.ProjectID, previous.CreatedOrdinal, current.CreatedOrdinal, sourceChangeBriefEffectCap+1,
	)
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
	if len(effects) == 0 && len(transitions) == 0 {
		return inject.SourceChangeBrief{}
	}
	roots, err := m.sessionRootRefs(ctx, sess)
	if err != nil {
		return inject.SourceChangeBrief{}
	}
	return assembleSourceChangeBrief(sourceChangeBriefInputs{
		Effects:     effects,
		Transitions: transitions,
		Truncated:   len(effects) > sourceChangeBriefEffectCap,
		SessionID:   sess.ID,
		Roots:       roots,
		Touched:     m.sessionTouchedPaths(ctx, sess, reader, roots),
	})
}

type sourceChangeBriefInputs struct {
	Effects     []sourceledger.Effect
	Transitions []sourceledger.GitTransition
	Truncated   bool
	SessionID   string
	Roots       []projectroot.RootRef
	Touched     map[string]bool
}

// assembleSourceChangeBrief itemizes working-set files and counts other changes.
// Observed ref movements precede their file effects.
func assembleSourceChangeBrief(in sourceChangeBriefInputs) inject.SourceChangeBrief {
	brief := inject.SourceChangeBrief{Truncated: in.Truncated}
	for i := len(in.Transitions) - 1; i >= 0; i-- {
		brief.Git = append(brief.Git, sourceGitLine(in.Roots, in.Transitions[i]))
	}
	effects := in.Effects
	if in.Truncated {
		effects = effects[:sourceChangeBriefEffectCap]
	}
	type fileGroup struct {
		newest  sourceledger.Effect
		effects int
	}
	groups := make(map[string]*fileGroup)
	order := make([]string, 0, len(effects))
	for _, effect := range effects {
		if effect.BranchID.IsWorker() {
			continue
		}
		if effect.ActorClassFor(in.SessionID) == sourceledger.ActorYou {
			continue
		}
		group, seen := groups[effect.FileID]
		if !seen {
			// Effects arrive newest first, so the first effect per file is its
			// newest and first-seen order is newest-file first.
			groups[effect.FileID] = &fileGroup{newest: effect, effects: 1}
			order = append(order, effect.FileID)
			continue
		}
		group.Effects++
	}
	for _, fileID := range order {
		group := groups[fileID]
		display := displaySourcePath(in.Roots, group.newest.RootID, group.newest.Path)
		if (!in.Touched[group.newest.Path] && !in.Touched[display]) || len(brief.Files) >= sourceChangeBriefFileCap {
			brief.OtherFiles++
			brief.OtherEffects += group.Effects
			continue
		}
		brief.Files = append(brief.Files, inject.SourceChangeFile{
			Path:    display,
			Actor:   string(group.newest.ActorClassFor(in.SessionID)),
			Detail:  group.newest.ActorDisplay(in.SessionID),
			Op:      string(group.newest.Op),
			At:      group.newest.TS.UTC().Format("15:04") + " UTC",
			Effects: group.Effects,
		})
	}
	return brief
}

// sessionTouchedPaths joins observed and authored paths. Failed lookups remain unclassified.
func (m *Manager) sessionTouchedPaths(
	ctx context.Context,
	sess *api.Session,
	reader sourceProvenanceReader,
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
				touched[displaySourcePath(roots, root.ID, path)] = true
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
		if root.ID == t.RootID && !root.IsPrimary && root.Label != "" {
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

// displaySourcePath renders a ledger (root, relative path) pair in the
// canonical display spelling: bare off the primary root, @label-qualified off
// any other, and unqualified when the root is unknown.
func displaySourcePath(roots []projectroot.RootRef, rootID, rel string) string {
	for _, root := range roots {
		if root.ID != rootID {
			continue
		}
		if root.IsPrimary || root.Label == "" {
			return rel
		}
		return "@" + root.Label + "/" + rel
	}
	return rel
}
