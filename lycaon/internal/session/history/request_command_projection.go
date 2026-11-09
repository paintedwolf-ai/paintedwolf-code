package history

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedCommand struct {
	Execution *messageview.ContinuationExecution `json:"execution"`
	Result    recall.HistoryRequest              `json:"result"`
	Handles   []string                           `json:"handles,omitempty"`
}

// projectCompletedCommands replaces whole settled exchanges, never executable call arguments.
func projectCompletedCommands(ctx context.Context, rows []api.Message, tokens ...int) []api.Message {
	out := make([]api.Message, 0, len(rows))
	for i := 0; i < len(rows); i++ {
		end, records := completedCommandExchange(rows, i)
		if end == i || compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(rows[i:end])) < 1024 ||
			len(tokens) > 0 && tokens[0] > 1 && compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(rows[i:])) <= tokens[0] {
			out = append(out, rows[i])
			continue
		}
		request := recordedHistoryRequest(rows[i])
		raw, _ := json.Marshal(request)     //nolint:errchkjson // History coordinates are plain values.
		encoded, _ := json.Marshal(records) //nolint:errchkjson // Records contain typed execution facts and coordinates.
		content, err := guidance.RenderGuidance(ctx, "compaction/command-exchange-reference", map[string]any{"history_json": string(raw), "records_json": string(encoded)})
		if err != nil || content == "" {
			out = append(out, rows[i])
			continue
		}
		projected := api.Message{ID: "host-command-record-" + rows[i].ID, Ord: rows[end-1].Ord,
			Role: api.MessageRoleAssistant, Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone,
			TrustTier: api.ContentTrustTierUntrusted, Content: content}
		out = append(out, projected)
		i = end - 1
	}
	return out
}

func completedCommandExchange(rows []api.Message, at int) (int, []recordedCommand) {
	row := rows[at]
	if row.Role != api.MessageRoleAssistant || row.ID == "" || row.Ord <= 0 || row.ContextPinned || len(row.ToolCalls) == 0 {
		return at, nil
	}
	pending := make(map[string]bool, len(row.ToolCalls))
	for _, call := range row.ToolCalls {
		if call.Name != "command" || call.ID == "" || pending[call.ID] {
			return at, nil
		}
		pending[call.ID] = true
	}
	var records []recordedCommand
	end := at + 1
	for end < len(rows) && len(pending) > 0 {
		result := rows[end]
		r := result.ToolResult
		if result.Role != api.MessageRoleTool || result.ID == "" || result.Ord <= 0 || result.ContextPinned || r == nil || !pending[r.ToolCallID] {
			return at, nil
		}
		execution := compaction.ContextMessageFromAPI(result).Execution
		if execution == nil || execution.ReceiptID == "" || execution.ProgramSource == "" || r.CommandProgram.SettledAt == nil {
			return at, nil
		}
		records = append(records, recordedCommand{Execution: execution, Result: recordedHistoryRequest(result), Handles: result.EvidenceHandles})
		delete(pending, r.ToolCallID)
		end++
	}
	if len(pending) > 0 {
		return at, nil
	}
	return end, records
}

func recordedHistoryRequest(row api.Message) recall.HistoryRequest {
	return recall.HistoryRequest{ThroughOrd: row.Ord, MessageID: row.ID, SourceSeq: row.Seq, Format: "text", BodyRunes: 4096}
}
