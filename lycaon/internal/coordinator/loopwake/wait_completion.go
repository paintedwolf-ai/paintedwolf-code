package loopwake

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// waitAlreadySatisfied returns a satisfied subscription without sleeping.
func waitAlreadySatisfied(status, reason string, triggerNames []string) (string, error) {
	conditions := make([]awaitstore.Condition, 0, len(triggerNames))
	for _, name := range triggerNames {
		if name != string(WaitTriggerTimer) {
			conditions = append(conditions, awaitstore.Condition{Kind: name})
		}
	}
	out, err := surveyjson.Marshal(WaitToolResult{
		Status: status, Reason: reason, Conditions: conditions,
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseWaitResumeArg(v any) bool {
	resume, _ := v.(bool)
	return resume
}

// WaitCompletionEndsCycle reads the host-stamped lifecycle outcome.
func WaitCompletionEndsCycle(toolName string, completion *api.ToolCompletion) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), "wait") && completion != nil &&
		completion.Operation == "wait" && completion.State == "parked"
}

// AskUserEndsCycle reports a pending successful ask.
func AskUserEndsCycle(toolName, toolContent string, succeeded bool) bool {
	if !succeeded || strings.TrimSpace(strings.ToLower(toolName)) != "ask_user" {
		return false
	}
	return toolResultStatus(toolContent) == "pending"
}

func toolResultStatus(content string) string {
	var result struct {
		Status string `json:"status"`
	}
	// Decode the leading JSON object; host guidance may follow it.
	if err := json.NewDecoder(strings.NewReader(content)).Decode(&result); err != nil {
		return ""
	}
	return strings.TrimSpace(result.Status)
}

// ParkForPendingUserInput waits for feedback or the ask deadline.
func (l *LoopEngine) ParkForPendingUserInput(ctx context.Context, sessionID, reason string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !l.sessionHasPendingUserInput(ctx, sessionID) {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "waiting for user ask"
	}
	until := l.pendingUserInputWaitDeadline(ctx, sessionID)
	// The timer bounds the park; user input ends it.
	l.EnterSleep(ctx, sessionID, until, reason, []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverUser)
	l.MarkWaitCalled(sessionID)
}

// ParkForHostObligation waits for the current host-held phase.
func (l *LoopEngine) ParkForHostObligation(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !l.sessionHostObligationHeld(ctx, sessionID) {
		return
	}
	overlayPromoteDue := l.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
	l.EnterSleep(
		ctx,
		sessionID,
		time.Now().UTC().Add(l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep()),
		l.hostObligationParkReason(ctx, sessionID),
		HostObligationWaitTriggers(overlayPromoteDue),
		nil,
		SleepMoverHost,
	)
	l.MarkWaitCalled(sessionID)
}
