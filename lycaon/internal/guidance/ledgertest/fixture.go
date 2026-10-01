package ledgertest

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

type pendingToolCall struct {
	name string
	args map[string]any
}

// BuildFromMessages constructs an evidence ledger from transcript fixtures.
func BuildFromMessages(projectDir string, messages []api.Message) evidence.Ledger {
	counters := map[string]int{}
	var pending []pendingToolCall
	var records []evidence.Record

	for _, msg := range messages {
		switch msg.Role {
		case api.MessageRoleAssistant:
			pending = pending[:0]
			for _, tc := range msg.ToolCalls {
				pending = append(pending, pendingToolCall{
					name: strings.TrimSpace(tc.Name),
					args: tc.Args,
				})
			}
		case api.MessageRoleTool:
			if len(pending) == 0 {
				continue
			}
			call := pending[0]
			pending = pending[1:]
			if !toolMessageSuccessful(msg) {
				continue
			}
			content := toolMessageContent(msg)
			record := evidence.BuildEvidenceRecord(projectDir, call.name, call.args, content)
			if record.Kind == "" {
				continue
			}
			record.SourceTool = call.name
			counters[record.Kind]++
			record.Handle = evidence.FormatHandle(record.Kind, counters[record.Kind])
			records = append(records, record)
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return evidence.AssembleLedger(records)
}

// ChildMessagesReader adapts child transcripts to LoadLedger.
func ChildMessagesReader(projectDir string, lookup func(string) []api.Message) guidance.EvidenceLedgerReader {
	return childMessagesReader{projectDir: projectDir, lookup: lookup}
}

// CloseoutReader serves a closeout's evidence: the dispatched legs and every
// session's transcript through lookup.
func CloseoutReader(projectDir string, legs []guidance.EvidenceLeg, lookup func(string) []api.Message) guidance.CloseoutEvidenceReader {
	return closeoutReader{childMessagesReader: childMessagesReader{projectDir: projectDir, lookup: lookup}, legs: legs}
}

type closeoutReader struct {
	childMessagesReader
	legs []guidance.EvidenceLeg
}

func (r closeoutReader) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return append([]guidance.EvidenceLeg(nil), r.legs...), nil
}

type childMessagesReader struct {
	projectDir string
	lookup     func(string) []api.Message
}

func (r childMessagesReader) LoadLedger(_ context.Context, sessionID string) (evidence.Ledger, error) {
	if r.lookup == nil {
		return evidence.Ledger{}, nil
	}
	msgs := r.lookup(sessionID)
	if len(msgs) == 0 {
		return evidence.Ledger{}, nil
	}
	return BuildFromMessages(r.projectDir, msgs), nil
}

func toolMessageContent(msg api.Message) string {
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		return stripHandleTag(msg.ToolResult.Content)
	}
	return stripHandleTag(strings.TrimSpace(msg.Content))
}

func stripHandleTag(content string) string {
	_, body := hostmarker.SplitEvidenceHandleTag(content)
	return body
}

// Unstamped fixture rows represent successful tool calls.
func toolMessageSuccessful(msg api.Message) bool {
	if msg.ToolResult == nil || msg.ToolResult.Outcome == "" {
		return true
	}
	return msg.ToolResult.Outcome == api.ToolResultOutcomeCompleted
}
