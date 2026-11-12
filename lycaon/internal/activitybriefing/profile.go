// Package activitybriefing explains an opened tool activity: why it ran and,
// once settled, what it changed or found.
package activitybriefing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	ProfileID      = "tool_activity"
	profileVersion = "1"
	// maxMembers bounds the activities and calls one card may explain.
	maxMembers = 64
	// pageLimit bounds the receipts read for one activity.
	pageLimit = 64
)

// Programs reads one host-composed activity and its receipts.
type Programs interface {
	ReadProgramActivityPage(ctx context.Context, sessionID, programID, activityID, cursor string, limit int) (wire.CommandProgramReceiptPage, error)
}

// Sessions confirms a session belongs to the project asking.
type Sessions interface {
	Get(ctx context.Context, id string) (*wire.Session, error)
}

type Profile struct {
	sessions Sessions
	programs Programs
	cause    briefing.CausalReader
}

var _ briefing.Profile = (*Profile)(nil)

func New(sessions Sessions, programs Programs, cause briefing.CausalReader) *Profile {
	return &Profile{sessions: sessions, programs: programs, cause: cause}
}

func (*Profile) ID() string              { return ProfileID }
func (*Profile) Version() string         { return profileVersion }
func (*Profile) Policy() briefing.Policy { return briefing.QueuedPolicy }

func decodeTarget(raw json.RawMessage) (wire.ToolActivityBriefingTarget, error) {
	var target wire.ToolActivityBriefingTarget
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return target, fmt.Errorf("%w: %v", briefing.ErrInvalidTarget, err)
	}
	if strings.TrimSpace(target.SessionID) == "" || len(target.ToolCallIds) == 0 {
		return target, fmt.Errorf("%w: session_id and tool_call_ids are required", briefing.ErrInvalidTarget)
	}
	for _, id := range target.ToolCallIds {
		if strings.TrimSpace(id) == "" {
			return target, fmt.Errorf("%w: tool_call_ids must not be empty", briefing.ErrInvalidTarget)
		}
	}
	for _, member := range target.Members {
		if strings.TrimSpace(member.ProgramID) == "" || strings.TrimSpace(member.ActivityID) == "" {
			return target, fmt.Errorf("%w: members need program_id and activity_id", briefing.ErrInvalidTarget)
		}
	}
	if len(target.ToolCallIds) > maxMembers || len(target.Members) > maxMembers {
		return target, fmt.Errorf("%w: at most %d members and calls", briefing.ErrTargetUnavailable, maxMembers)
	}
	return target, nil
}

// targetKey identifies the card: its session, members, calls and revision.
func targetKey(target wire.ToolActivityBriefingTarget) (string, string, error) {
	canonical, err := json.Marshal(target)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), string(canonical), nil
}

// Resolve reads the card's host facts and builds its sections. The client's
// target names records; every one is read back from the host.
func (p *Profile) Resolve(ctx context.Context, target briefing.Target) (briefing.Request, error) {
	wanted, err := decodeTarget(target.Ref)
	if err != nil {
		return briefing.Request{}, err
	}
	session, err := p.sessions.Get(ctx, wanted.SessionID)
	if err != nil {
		return briefing.Request{}, err
	}
	if session == nil || session.ProjectID != target.ProjectID {
		return briefing.Request{}, store.ErrSessionNotFound
	}
	evidence, err := p.read(ctx, wanted)
	if err != nil {
		return briefing.Request{}, err
	}
	key, canonical, err := targetKey(wanted)
	if err != nil {
		return briefing.Request{}, err
	}
	sections, err := buildSections(ctx, evidence)
	if err != nil {
		return briefing.Request{}, err
	}
	return briefing.Request{
		ProjectID: target.ProjectID, ProjectDir: target.ProjectDir, ProfileID: ProfileID, Version: profileVersion,
		TargetKey: key, TargetKind: "activity", TargetJSON: canonical, ScopeFingerprint: key,
		Manual: target.Manual, Policy: p.Policy(), Sections: sections,
		Action: &briefing.ActionRef{SessionID: wanted.SessionID, ToolCallID: wanted.ToolCallIds[0]},
	}, nil
}

// evidence is everything one card's sections read.
type evidence struct {
	target     wire.ToolActivityBriefingTarget
	facts      facts
	executions []wire.CommandExecutionReceipt
	cause      briefing.CausalRead
}

func (p *Profile) read(ctx context.Context, target wire.ToolActivityBriefingTarget) (evidence, error) {
	out := evidence{target: target, facts: facts{command: len(target.Members) > 0}}
	for _, member := range target.Members {
		page, err := p.programs.ReadProgramActivityPage(ctx, target.SessionID, member.ProgramID, member.ActivityID, "", pageLimit)
		if err != nil {
			return out, err
		}
		activity, ok := findActivity(page.Activities, member.ActivityID)
		if !ok {
			return out, fmt.Errorf("%w: activity %s is not in its program", briefing.ErrTargetUnavailable, member.ActivityID)
		}
		out.facts.activities = append(out.facts.activities, activity)
		out.executions = append(out.executions, page.Executions...)
	}
	read, err := p.cause.ReadActionCause(ctx, briefing.ActionRef{SessionID: target.SessionID, ToolCallID: target.ToolCallIds[0]})
	if err != nil {
		return out, err
	}
	// A call the transcript does not hold would leave the model nothing to
	// explain, so the target is refused instead of summarized.
	if !read.Found {
		return out, fmt.Errorf("%w: call %s is not in the session", briefing.ErrTargetUnavailable, target.ToolCallIds[0])
	}
	out.cause = read
	out.facts.settled = settled(out)
	return out, nil
}

func findActivity(activities []wire.CommandActivity, id string) (wire.CommandActivity, bool) {
	for _, activity := range activities {
		if activity.ID == id {
			return activity, true
		}
	}
	return wire.CommandActivity{}, false
}

// settled reports a card whose every activity, or whose call, has ended.
func settled(ev evidence) bool {
	if !ev.facts.command {
		return ev.cause.Call.Result != nil
	}
	for _, activity := range ev.facts.activities {
		switch activity.State {
		case "completed", "failed", "refused", "interrupted":
		default:
			return false
		}
	}
	return true
}
