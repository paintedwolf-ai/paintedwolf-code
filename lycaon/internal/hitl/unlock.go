package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrPresenceNotRequired refuses an unlock challenge for an option that sends
// no held value, or whose chat is already unlocked.
var ErrPresenceNotRequired = errors.New("this option needs no confirmation")

// UnlockRecorder audits an unlock inside the approval that opened it.
type UnlockRecorder interface {
	RecordUnlockTx(ctx context.Context, tx *sql.Tx, projectID string, unlock presence.Unlock) error
}

// vaultUnlock wires presence verification and the chats' unlocks it opens.
type vaultUnlock struct {
	broker   *presence.Broker
	unlocks  *presence.Unlocks
	recorder UnlockRecorder
}

// SetVaultUnlock wires the presence broker, the chats' unlocks, and their
// audit. Without them no held value can be approved while its chat is locked.
func (m *VaultPresence) SetVaultUnlock(broker *presence.Broker, unlocks *presence.Unlocks, recorder UnlockRecorder) {
	if m != nil {
		m.vault = vaultUnlock{broker: broker, unlocks: unlocks, recorder: recorder}
	}
}

// PresenceAvailable reports whether this device can verify presence.
func (m *VaultPresence) PresenceAvailable() bool {
	return m != nil && m.vault.broker.Available() && m.vault.unlocks != nil && m.vault.recorder != nil
}

// UnlockChallenge is a presence challenge for one approving option.
type UnlockChallenge struct {
	presence.Challenge
	Prompt string
}

// UnlockSubject is what an unlock challenge signs. The desktop shell reads
// session_id, checkpoint_id, and option_id back before asking the person.
type UnlockSubject struct {
	SessionID     string `json:"session_id"`
	CheckpointID  string `json:"checkpoint_id"`
	OptionID      string `json:"option_id"`
	PlanID        string `json:"plan_id"`
	ChatSessionID string `json:"chat_session_id"`
}

// BeginUnlockChallenge binds presence to one pending option that sends held
// values while their chat is locked, the plan that offers it, and the
// deciding person. An unlocked chat needs no challenge.
func (m *VaultPresence) BeginUnlockChallenge(ctx context.Context, sessionID, checkpointID, optionID, windowLabel string) (UnlockChallenge, error) {
	// An unknown or settled checkpoint answers as such before presence is consulted.
	plan, option, err := m.pendingHeldOption(ctx, sessionID, checkpointID, optionID)
	if err != nil {
		return UnlockChallenge{}, err
	}
	if m.unlocked(plan.Held) {
		return UnlockChallenge{}, ErrPresenceNotRequired
	}
	if !m.PresenceAvailable() {
		return UnlockChallenge{}, presence.ErrUnavailable
	}
	person, err := personResolution(ctx)
	if err != nil {
		return UnlockChallenge{}, err
	}
	challenge, err := m.vault.broker.Begin(presence.Claim{
		Purpose: presence.PurposeUnlock, PersonID: person.PersonID, WindowLabel: windowLabel,
		Key: checkpointID + "\x00" + option.ID, Subject: unlockSubject(sessionID, checkpointID, plan, option),
	})
	if err != nil {
		return UnlockChallenge{}, err
	}
	return UnlockChallenge{Challenge: challenge, Prompt: unlockPrompt(plan.Held)}, nil
}

// unlocked reports whether held's chat is unlocked.
func (m *VaultPresence) unlocked(held *HeldRelease) bool {
	_, open := m.vault.unlocks.Active(held.ChatSessionID)
	return open
}

// pendingHeldOption loads a pending tool approval's plan and an option that
// would send its held values.
func (m *VaultPresence) pendingHeldOption(ctx context.Context, sessionID, checkpointID, optionID string) (ApprovalPlan, ApprovalOption, error) {
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

func unlockSubject(sessionID, checkpointID string, plan ApprovalPlan, option ApprovalOption) UnlockSubject {
	return UnlockSubject{
		SessionID: sessionID, CheckpointID: checkpointID, OptionID: option.ID, PlanID: plan.ID,
		ChatSessionID: plan.Held.ChatSessionID,
	}
}

// unlockPrompt is the host-authored reason the operating system shows.
func unlockPrompt(held *HeldRelease) string {
	names := presence.PromptText(strings.Join(held.Names(), ", "))
	return fmt.Sprintf("Unlock %s for this chat in Painted Wolf Code.", names)
}

// heldAnswer decides how an approving answer that sends held values may
// proceed: an unlocked chat needs nothing more, a locked one needs the
// resolver's verified presence, which unlocks it. Only a person
// answers for held values, never a policy.
func (m *VaultPresence) heldAnswer(
	sessionID, checkpointID string, plan ApprovalPlan, option ApprovalOption, resolver ApprovalResolver, resolution Resolution,
) (*presence.Unlock, error) {
	if resolver.policy != nil || strings.TrimSpace(resolution.PersonID) == "" {
		return nil, ErrPresenceRequired
	}
	if resolver.proof == nil {
		if m.unlocked(plan.Held) {
			return nil, nil
		}
		return nil, ErrPresenceRequired
	}
	if !m.PresenceAvailable() {
		return nil, presence.ErrUnavailable
	}
	verified, err := m.vault.broker.Complete(*resolver.proof, presence.PurposeUnlock, resolution.PersonID)
	if err != nil {
		return nil, err
	}
	signed, err := json.Marshal(unlockSubject(sessionID, checkpointID, plan, option))
	if err != nil {
		return nil, fmt.Errorf("encode unlock subject: %w", err)
	}
	var got, want any
	if json.Unmarshal(verified.Subject, &got) != nil || json.Unmarshal(signed, &want) != nil || !jsonEqual(got, want) {
		return nil, presence.ErrDenied
	}
	unlock := presence.NewUnlock(plan.Held.ChatSessionID, verified)
	return &unlock, nil
}

// answerUnlockCards settles the chat's other pending unlock-only cards once
// presence on one approval unlocked it: they asked only that, and the person
// just answered it, so nobody confirms the same thing twice. Cards that also
// approve a recipient stay open; their question is still unanswered.
func (m *VaultPresence) answerUnlockCards(ctx context.Context, unlock presence.Unlock, answeredID string) {
	kind := api.CheckpointKindToolApproval
	rows, err := m.store.ListPendingForParent(ctx, unlock.ChatSessionID, &kind)
	if err != nil {
		slog.WarnContext(ctx, "pending unlock cards could not be listed", "chat_session_id", unlock.ChatSessionID, "error", err)
		return
	}
	for _, row := range rows {
		if row.ID == answeredID {
			continue
		}
		raw, _ := row.Payload["approval_plan"].(map[string]any)
		plan, err := approvalPlanFromMap(raw)
		if err != nil || plan.Held.empty() || !plan.Held.UnlockOnly || plan.Held.ChatSessionID != unlock.ChatSessionID {
			continue
		}
		result := DecisionResult{Approved: true, OptionID: unlockOptionID, Comments: "unlocked by confirming another approval in this chat"}
		resolution := Resolution{By: authzledger.ResolvedByHuman, PersonID: unlock.PersonID}
		if err := m.checkpoints.settlePending(ctx, row.ID, DecisionStatusApproved, result, resolution); err != nil {
			slog.WarnContext(ctx, "unlock card could not be settled", "checkpoint_id", row.ID, "error", err)
		}
	}
}

func jsonEqual(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

func (m *VaultPresence) RecordUnlockTx(ctx context.Context, tx *sql.Tx, projectID string, unlock presence.Unlock) error {
	return m.vault.recorder.RecordUnlockTx(ctx, tx, projectID, unlock)
}

func (m *VaultPresence) CommitUnlock(ctx context.Context, unlock presence.Unlock, answeredID string) {
	m.vault.unlocks.Open(unlock)
	m.answerUnlockCards(ctx, unlock, answeredID)
}

type VaultPresence struct {
	store       vaultCheckpointStore
	checkpoints checkpointSettlement
	vault       vaultUnlock
}

type vaultCheckpointStore interface {
	Get(context.Context, string) (*StoredCheckpoint, error)
	ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]StoredCheckpoint, error)
}
