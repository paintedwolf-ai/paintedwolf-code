package loopwake

import (
	"encoding/json"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
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
