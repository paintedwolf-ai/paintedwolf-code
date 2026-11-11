package stepbriefing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const profileVersion = "1"

// Ledger reads the durable records a step explanation uses.
type Ledger interface {
	EffectsByID(ctx context.Context, projectID string, ids []string) ([]sourceledger.Effect, error)
	EffectsInGroup(ctx context.Context, projectID, commandWindowID, gitTransitionID string) ([]sourceledger.Effect, error)
	CommandWindows(ctx context.Context, ids []string) (map[string]sourceledger.CommandWindow, error)
	GitTransitionsByIDs(ctx context.Context, ids []string) (map[string]sourceledger.GitTransition, error)
	CompareEffect(ctx context.Context, projectID, effectID string) (sourceledger.Comparison, error)
}

type Profile struct {
	ledger Ledger
	cause  briefing.CausalReader
}

var _ briefing.Profile = (*Profile)(nil)

func New(ledger Ledger, cause briefing.CausalReader) *Profile {
	return &Profile{ledger: ledger, cause: cause}
}

func (*Profile) ID() string              { return ProfileID }
func (*Profile) Version() string         { return profileVersion }
func (*Profile) Policy() briefing.Policy { return briefing.QueuedPolicy }

func decodeTarget(raw json.RawMessage) (wire.SourceStepBriefingTarget, error) {
	var target wire.SourceStepBriefingTarget
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return target, fmt.Errorf("%w: %v", briefing.ErrInvalidTarget, err)
	}
	if strings.TrimSpace(target.TargetKey) == "" {
		return target, fmt.Errorf("%w: target_key is required", briefing.ErrInvalidTarget)
	}
	switch target.Kind {
	case "effect", "command", "git":
	case "outside":
		if len(target.EffectIds) < 2 {
			return target, fmt.Errorf("%w: an outside group names at least two effects", briefing.ErrInvalidTarget)
		}
		if len(target.EffectIds) > maxOutsideMembers {
			return target, fmt.Errorf("%w: at most %d effects", briefing.ErrTargetUnavailable, maxOutsideMembers)
		}
	default:
		return target, fmt.Errorf("%w: unknown kind %q", briefing.ErrInvalidTarget, target.Kind)
	}
	return target, nil
}

// step is a resolved target: its effects and the call that made them.
type step struct {
	target  wire.SourceStepBriefingTarget
	effects []sourceledger.Effect
	// window is the command window of a command step.
	window *sourceledger.CommandWindow
	// git is the movement of a Git step.
	git *sourceledger.GitTransition
	// session and call name the action behind the step, when one exists.
	session, call string
}

// Resolve reads the step from the ledger by its own identity and checks the
// client's descriptor against it; the descriptor is never trusted.
func (p *Profile) Resolve(ctx context.Context, target briefing.Target) (briefing.Request, error) {
	wanted, err := decodeTarget(target.Ref)
	if err != nil {
		return briefing.Request{}, err
	}
	resolved, err := p.read(ctx, target.ProjectID, wanted)
	if err != nil {
		return briefing.Request{}, err
	}
	if got := resolved.fingerprint(); got != wanted.ScopeFingerprint {
		return briefing.Request{}, fmt.Errorf("%w: the step changed since it was listed", briefing.ErrTargetUnavailable)
	}
	sections, err := p.sections(ctx, target.ProjectID, resolved)
	if err != nil {
		return briefing.Request{}, err
	}
	canonical, err := json.Marshal(wanted)
	if err != nil {
		return briefing.Request{}, err
	}
	return briefing.Request{
		ProjectID: target.ProjectID, ProjectDir: target.ProjectDir, ProfileID: ProfileID, Version: profileVersion,
		TargetKey: wanted.TargetKey, TargetKind: wanted.Kind, TargetJSON: string(canonical), ScopeFingerprint: wanted.ScopeFingerprint,
		Manual: target.Manual, Policy: p.Policy(), Sections: sections, Action: resolved.action(),
	}, nil
}

func (p *Profile) read(ctx context.Context, projectID string, target wire.SourceStepBriefingTarget) (step, error) {
	out := step{target: target}
	var err error
	switch target.Kind {
	case "effect":
		out.effects, err = p.ledger.EffectsByID(ctx, projectID, []string{target.TargetKey})
	case "outside":
		out.effects, err = p.ledger.EffectsByID(ctx, projectID, target.EffectIds)
	case "command":
		id := strings.TrimPrefix(target.TargetKey, "command:")
		windows, readErr := p.ledger.CommandWindows(ctx, []string{id})
		if readErr != nil {
			return out, readErr
		}
		window, ok := windows[id]
		if !ok || window.ProjectID != projectID {
			return out, fmt.Errorf("%w: unknown command window", briefing.ErrTargetUnavailable)
		}
		if window.State == wire.SourceCommandWindowRunning {
			return out, fmt.Errorf("%w: the command is still running", briefing.ErrTargetUnavailable)
		}
		out.window, out.session, out.call = &window, window.SessionID, window.ToolCallID
		out.effects, err = p.ledger.EffectsInGroup(ctx, projectID, id, "")
	case "git":
		id := strings.TrimPrefix(target.TargetKey, "git:")
		changes, readErr := p.ledger.GitTransitionsByIDs(ctx, []string{id})
		if readErr != nil {
			return out, readErr
		}
		change, ok := changes[id]
		if !ok || change.ProjectID != projectID {
			return out, fmt.Errorf("%w: unknown Git movement", briefing.ErrTargetUnavailable)
		}
		out.git, out.session, out.call = &change, change.SessionID, change.ToolCallID
		out.effects, err = p.ledger.EffectsInGroup(ctx, projectID, "", id)
	}
	if errors.Is(err, sourceledger.ErrGroupTooLarge) {
		return out, fmt.Errorf("%w: %v", briefing.ErrTargetUnavailable, err)
	}
	if err != nil {
		return out, err
	}
	if target.Kind == "effect" || target.Kind == "outside" {
		if len(out.effects) != max(1, len(target.EffectIds)) {
			return out, fmt.Errorf("%w: unknown effect", briefing.ErrTargetUnavailable)
		}
		if target.Kind == "effect" {
			out.session, out.call = out.effects[0].SessionID, out.effects[0].ToolCallID
		}
	}
	return out, nil
}

func (s step) action() *briefing.ActionRef {
	if s.session == "" || s.call == "" {
		return nil
	}
	return &briefing.ActionRef{SessionID: s.session, ToolCallID: s.call}
}

// fingerprint recomputes the frozen scope the rail listed.
func (s step) fingerprint() string {
	switch s.target.Kind {
	case "effect":
		return fingerprint(s.effects[0].ID, s.effects[0].AfterVersionID)
	default:
		ids := make([]string, len(s.effects))
		for i, effect := range s.effects {
			ids[i] = effect.ID
		}
		return fingerprint(append([]string{s.target.TargetKey}, ids...)...)
	}
}

// ledgerParts reads through the ledger's walk, command, Git and comparison
// stores.
type ledgerParts struct {
	*sourceledger.Walk
	*sourceledger.Commands
	*sourceledger.Git
	*sourceledger.Comparisons
}

// LedgerOf is the profile's view of a source ledger.
func LedgerOf(store *sourceledger.Store) Ledger {
	return ledgerParts{Walk: store.Walk, Commands: store.Commands, Git: store.Git, Comparisons: store.Comparisons}
}
