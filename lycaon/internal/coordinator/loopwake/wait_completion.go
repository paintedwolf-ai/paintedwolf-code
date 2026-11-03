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

// ParkedCompletionEndsCycle reports a call that parked its turn on the
// session's wait lease, as the host stamped it.
func ParkedCompletionEndsCycle(completion *api.ToolCompletion) bool {
	return completion != nil && completion.State == parkedState && completion.ResourceKind == waitResourceKind
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
