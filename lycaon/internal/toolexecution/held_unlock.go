package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolsecrets"

	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// presenceAvailable reports whether this device can verify presence.
func (e *Secrets) presenceAvailable() bool {
	return e != nil && e.presenceAvailableFn != nil && e.presenceAvailableFn()
}

// SetPresenceAvailable reports whether a card can collect verified presence.
func (e *Secrets) SetPresenceAvailable(available func() bool) {
	if e != nil {
		e.presenceAvailableFn = available
	}
}

// SetVaultUnlocks wires the chats' unlocks that let held values leave.
func (e *Secrets) SetVaultUnlocks(unlocks *presence.Unlocks) {
	if e != nil {
		e.vaultUnlocks = unlocks
	}
}

// heldRelease names the person-held values a card would send and the chat
// whose unlock they leave under.
func heldRelease(finding secretmatch.Alert, custody secretcap.CustodySummary, recipients []secretmatch.Recipient) *hitl.HeldRelease {
	if len(custody.Held) == 0 {
		return nil
	}
	held := &hitl.HeldRelease{
		ProjectID: finding.ProjectID, ChatSessionID: secretScreenChatSession(finding),
		Recipients: append([]secretmatch.Recipient(nil), recipients...),
	}
	for _, value := range custody.Held {
		held.Secrets = append(held.Secrets, hitl.HeldSecret{SecretID: value.SecretID, Version: value.Version, Name: value.Name})
	}
	return held
}

// ensureHeldUnlocked lets an approved send proceed once its held values'
// chat is unlocked. Approval already covers the recipients, so a locked chat
// raises only the card that unlocks it; every send waiting on the same chat
// joins that one card.
func (e *Secrets) ensureHeldUnlocked(
	ctx context.Context, finding secretmatch.Alert, custody secretcap.CustodySummary, recipients []secretmatch.Recipient,
) (secretmatch.Resolution, error) {
	held := heldRelease(finding, custody, recipients)
	if held == nil {
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	switch e.awaitPendingUnlock(ctx, held.ChatSessionID) {
	case unlockWaitUnlocked:
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	case unlockWaitRefused:
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	case unlockWaitLocked:
	}
	if e.Approvals.checkpointMgr == nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageCheckpointsUnwired, nil)
	}
	action := hitl.ProposedAction{
		Tool: string(finding.Surface), EstimatedImpact: "Unlocks values you stored for this chat",
		SessionID: finding.SessionID, RootSessionID: finding.RootSessionID,
		ProjectID: finding.ProjectID, ProjectDir: finding.ProjectDir,
	}
	screen := toolsecrets.SecretReviewPayload(finding, recipients, false)
	screen.Held = held
	plan, err := hitl.NewUnlockPlan(action, *held, screen)
	if err != nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageRaise, err)
	}
	final, err := e.Approvals.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action: action, Plan: plan, Title: hitl.TitleUnlockForThisChat,
		ToolCallID: finding.ToolCallID, ProjectID: finding.ProjectID,
		SecretScreen: screen, SecretScreenHit: true, SkipGrantOfferAutofill: true,
		CoalesceKey: "vault-unlock:" + held.ChatSessionID,
	})
	if err != nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageRaise, err)
	}
	return e.resolveSecretScreenDecision(ctx, finding, final)
}

// heldAskCounter counts each chat's open cards that would unlock it when
// approved with presence, and how many of them the person refused.
type heldAskCounter struct {
	mu       sync.Mutex
	byChat   map[string]int
	refusals map[string]int
}

// open records a card; closed reports whether the person refused it.
func (c *heldAskCounter) open(chatSessionID string) (closed func(refused bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChat == nil {
		c.byChat, c.refusals = make(map[string]int), make(map[string]int)
	}
	c.byChat[chatSessionID]++
	return func(refused bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if refused {
			c.refusals[chatSessionID]++
		}
		if c.byChat[chatSessionID]--; c.byChat[chatSessionID] <= 0 {
			delete(c.byChat, chatSessionID)
		}
	}
}

// state reports whether a card is open in the chat and how many refusals it
// has seen.
func (c *heldAskCounter) state(chatSessionID string) (pending bool, refusals int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byChat[chatSessionID] > 0, c.refusals[chatSessionID]
}

// heldAskPoll is how often a waiting send rechecks the chat; both checks
// read memory.
const heldAskPoll = 100 * time.Millisecond

// unlockWait is how a send waiting behind the chat's open cards ended.
type unlockWait int

const (
	unlockWaitLocked unlockWait = iota
	unlockWaitUnlocked
	// unlockWaitRefused: the person said no to a card this send waited on.
	unlockWaitRefused
)

// awaitPendingUnlock waits out any card already open in the chat whose
// approval would unlock it. The person answers that card once: an approval
// that unlocks the chat lets every send behind it follow without a card of
// its own, and a refusal holds them all.
func (e *Secrets) awaitPendingUnlock(ctx context.Context, chatSessionID string) unlockWait {
	_, refusalsBefore := e.heldAsks.state(chatSessionID)
	ticker := time.NewTicker(heldAskPoll)
	defer ticker.Stop()
	for {
		if _, open := e.vaultUnlocks.Active(chatSessionID); open {
			return unlockWaitUnlocked
		}
		pending, refusals := e.heldAsks.state(chatSessionID)
		if refusals > refusalsBefore {
			return unlockWaitRefused
		}
		if !pending {
			return unlockWaitLocked
		}
		select {
		case <-ctx.Done():
			return unlockWaitLocked
		case <-ticker.C:
		}
	}
}
