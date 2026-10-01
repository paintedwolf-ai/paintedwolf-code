package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/pkg/api"
)

// coordinatorDraftSlotEligible identifies turns that own a draft rail.
func coordinatorDraftSlotEligible(sess *api.Session, profileID string) bool {
	if sess == nil || sess.IsWorkerChild() {
		return false
	}
	return guard.IsCoordinatorProfile(profileID)
}

// ensureCoordinatorDraftSlot keeps guard retries on one draft rail.
func (st *promptLoopTurnState) ensureCoordinatorDraftSlot() string {
	if st == nil {
		return ""
	}
	if st.draftSlotID == "" {
		st.draftSlotID = uuid.NewString()
		st.draftSlotAppended = false
	}
	return st.draftSlotID
}

// closeCoordinatorDraftSlot closes only the matching committed draft.
func (st *promptLoopTurnState) closeCoordinatorDraftSlot(messageID string) {
	if st == nil || st.draftSlotID == "" {
		return
	}
	if strings.TrimSpace(messageID) != st.draftSlotID {
		return
	}
	st.draftSlotID = ""
	st.draftSlotAppended = false
}

func (st *promptLoopTurnState) usesCoordinatorDraftSlot(assistantMessageID string) bool {
	if st == nil || st.draftSlotID == "" {
		return false
	}
	return strings.TrimSpace(assistantMessageID) == st.draftSlotID
}

func (l *PromptLoop) draftVersionCount(ctx context.Context, sessionID, slotID string) (int, error) {
	if l == nil || l.Deps.CountDraftVersions == nil {
		return 0, nil
	}
	sidecar, err := l.Deps.CountDraftVersions(ctx, sessionID, slotID)
	if err != nil {
		return 0, err
	}
	return sidecar + 1, nil
}

func (l *PromptLoop) stampDraftVersionCount(ctx context.Context, sessionID string, msg *api.Message) error {
	if l == nil || msg == nil || msg.ID == "" {
		return nil
	}
	count, err := l.draftVersionCount(ctx, sessionID, msg.ID)
	if err != nil {
		return err
	}
	msg.DraftVersionCount = count
	return nil
}

func (l *PromptLoop) maybeWithdrawCoordinatorDraft(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	st *promptLoopTurnState,
) error {
	content := ""
	if st != nil {
		content = st.lastAssistantContent
	}
	return l.withdrawCoordinatorDraft(ctx, sess, sessionID, st, content)
}

func (l *PromptLoop) withdrawCoordinatorDraft(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	st *promptLoopTurnState,
	content string,
) error {
	if l == nil || st == nil || st.draftSlotID == "" || !st.draftSlotAppended {
		return nil
	}
	if l.Deps.UpdateMessage == nil {
		return fmt.Errorf("update message not configured")
	}
	patch := newProvisionalAssistantMessage(api.Message{
		ID:          st.draftSlotID,
		Content:     strings.TrimSpace(content),
		DraftStatus: api.DraftStatusWithdrawn,
	})
	patch.Visibility = api.MessageVisibilityTranscript
	patch.Kind = api.MessageKindDraft
	if err := l.stampDraftVersionCount(ctx, sessionID, &patch); err != nil {
		return err
	}
	if err := l.Deps.UpdateMessage(ctx, sessionID, st.draftSlotID, patch); err != nil {
		return err
	}
	return l.closeCoordinatorDraftSlot(ctx, sessionID, st, st.draftSlotID)
}
