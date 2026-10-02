package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrPresenceNotRequired refuses a release challenge for an option that
// releases no held value.
var ErrPresenceNotRequired = errors.New("this option releases no value a person holds")

// heldRelease wires presence verification for plans that release held values.
type heldRelease struct {
	broker   *presence.Broker
	ledger   *presence.ReleaseLedger
	recorder ReleaseRecorder
}

// SetHeldRelease wires the presence broker, the vault release ledger, and the
// audit recorder. Without them no plan that releases held values can be
// approved.
func (m *Manager) SetHeldRelease(broker *presence.Broker, ledger *presence.ReleaseLedger, recorder ReleaseRecorder) {
	if m != nil {
		m.held = heldRelease{broker: broker, ledger: ledger, recorder: recorder}
	}
}

// PresenceAvailable reports whether this device can verify presence.
func (m *Manager) PresenceAvailable() bool {
	return m != nil && m.held.broker.Available() && m.held.ledger != nil && m.held.recorder != nil
}

// ReleaseChallenge is a presence challenge for one approving option.
type ReleaseChallenge struct {
	presence.Challenge
	Prompt string
}

// BeginReleaseChallenge binds presence to one pending option that releases
// held values, the exact plan that offers it, and the deciding person.
func (m *Manager) BeginReleaseChallenge(ctx context.Context, sessionID, checkpointID, optionID, windowLabel string) (ReleaseChallenge, error) {
	// An unknown or settled checkpoint answers as such before presence is consulted.
	plan, option, err := m.pendingReleaseOption(ctx, sessionID, checkpointID, optionID)
	if err != nil {
		return ReleaseChallenge{}, err
	}
	if !m.PresenceAvailable() {
		return ReleaseChallenge{}, presence.ErrUnavailable
	}
	person, err := personResolution(ctx)
	if err != nil {
		return ReleaseChallenge{}, err
	}
	challenge, err := m.held.broker.Begin(presence.Claim{
		Purpose: presence.PurposeRelease, PersonID: person.PersonID, WindowLabel: windowLabel,
		Key: checkpointID + "\x00" + option.ID, Subject: releaseSubject(sessionID, checkpointID, plan, option),
	})
	if err != nil {
		return ReleaseChallenge{}, err
	}
	return ReleaseChallenge{Challenge: challenge, Prompt: heldReleasePrompt(plan.Held, option)}, nil
}

// pendingReleaseOption loads a pending tool approval's plan and an option
// that would release its held values.
func (m *Manager) pendingReleaseOption(ctx context.Context, sessionID, checkpointID, optionID string) (ApprovalPlan, ApprovalOption, error) {
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return ApprovalPlan{}, ApprovalOption{}, err
	}
	if row.SessionID != sessionID {
		return ApprovalPlan{}, ApprovalOption{}, ErrCheckpointNotFound
	}
	if row.Kind != api.CheckpointKindToolApproval {
		return ApprovalPlan{}, ApprovalOption{}, ErrCheckpointKindMismatch
	}
	if row.Status != DecisionStatusPending {
		return ApprovalPlan{}, ApprovalOption{}, ErrCheckpointNotPending
	}
	raw, _ := row.Payload["approval_plan"].(map[string]any)
	plan, err := approvalPlanFromMap(raw)
	if err != nil {
		return ApprovalPlan{}, ApprovalOption{}, fmt.Errorf("approval plan: %w", err)
	}
	option, ok := plan.Option(optionID)
	if !ok {
		return ApprovalPlan{}, ApprovalOption{}, ErrApprovalOptionNotFound
	}
	if option.Disabled {
		return ApprovalPlan{}, ApprovalOption{}, ErrApprovalOptionUnavailable
	}
	if !plan.releasesHeld(option) {
		return ApprovalPlan{}, ApprovalOption{}, ErrPresenceNotRequired
	}
	return *plan, option, nil
}

func releaseSubject(sessionID, checkpointID string, plan ApprovalPlan, option ApprovalOption) ReleaseSubject {
	return ReleaseSubject{
		SessionID: sessionID, CheckpointID: checkpointID, OptionID: option.ID, PlanID: plan.ID,
		Secrets: plan.Held.Secrets, RecipientSet: secretmatch.RecipientDigest(plan.Held.Recipients),
	}
}

// verifiedRelease is a consumed release challenge that matched its plan.
type verifiedRelease struct {
	attestation presence.Attestation
	windowLabel string
}

// verifyRelease consumes the resolver's proof against exactly this pending
// option. A policy, or a person without verified presence, cannot release
// held values.
func (m *Manager) verifyRelease(
	sessionID, checkpointID string, plan ApprovalPlan, option ApprovalOption, resolver ApprovalResolver, resolution Resolution,
) (verifiedRelease, error) {
	if resolver.policy != nil || resolver.proof == nil || strings.TrimSpace(resolution.PersonID) == "" {
		return verifiedRelease{}, ErrPresenceRequired
	}
	if !m.PresenceAvailable() {
		return verifiedRelease{}, presence.ErrUnavailable
	}
	verified, err := m.held.broker.Complete(*resolver.proof, presence.PurposeRelease, resolution.PersonID)
	if err != nil {
		return verifiedRelease{}, err
	}
	signed, err := json.Marshal(releaseSubject(sessionID, checkpointID, plan, option))
	if err != nil {
		return verifiedRelease{}, fmt.Errorf("encode release subject: %w", err)
	}
	var got, want any
	if json.Unmarshal(verified.Subject, &got) != nil || json.Unmarshal(signed, &want) != nil || !jsonEqual(got, want) {
		return verifiedRelease{}, presence.ErrDenied
	}
	return verifiedRelease{
		attestation: presence.Attestation{
			ID: verified.ChallengeID, PersonID: verified.PersonID, Authenticator: verified.Authenticator,
			AttestedAt: verified.VerifiedAt,
		},
		windowLabel: verified.WindowLabel,
	}, nil
}

func jsonEqual(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

// forgetAttested removes ledger entries a failed approval recorded.
func (m *Manager) forgetAttested(option ApprovalOption) {
	for _, delta := range option.Authority {
		if delta.Grant != nil && delta.Grant.Attestation != nil {
			_ = m.held.ledger.Forget(delta.Grant.ID)
		}
	}
}
