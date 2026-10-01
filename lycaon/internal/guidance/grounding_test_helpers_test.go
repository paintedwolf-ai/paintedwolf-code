package guidance_test

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/evidence"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func unionLegEvidence(projectDir string, legs []guidance.EvidenceLeg, lookup func(string) []api.Message) guidance.CloseoutEvidence {
	ev, err := guidance.UnionLegEvidence(context.Background(), ledgertest.ChildMessagesReader(projectDir, lookup), legs)
	if err != nil {
		panic(err)
	}
	return ev
}

func evidenceWithObservedPaths(paths ...string) evidence.Ledger {
	return buildLedgerFromMessages("", paths...)
}

func buildLedgerFromMessages(projectDir string, paths ...string) evidence.Ledger {
	if len(paths) == 0 {
		return ledgertest.BuildFromMessages(projectDir, nil)
	}
	calls := make([]api.ToolCall, 0, len(paths))
	for i, p := range paths {
		calls = append(calls, api.ToolCall{
			Name: "read",
			ID:   fmt.Sprintf("c%d", i+1),
			Args: map[string]any{"path": p},
		})
	}
	msgs := []api.Message{{Role: api.MessageRoleAssistant, ToolCalls: calls}}
	for range calls {
		msgs = append(msgs, api.Message{
			Role:       api.MessageRoleTool,
			Content:    "ok",
			ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "ok"},
		})
	}
	return ledgertest.BuildFromMessages(projectDir, msgs)
}
