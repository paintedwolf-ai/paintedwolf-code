package reporting

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// findingAgentCoordinator is the default author for a coordinator-authored finding.
const findingAgentCoordinator = "coordinator"

// FindingGroundingGate audits a finding's summary/ref against the evidence ledger.
type FindingGroundingGate interface {
	AuditFinding(ctx context.Context, summary, ref string, tctx tools.ToolContext) error
}

// RecordFindingGates screens evidence before storage.
type RecordFindingGates struct{ Grounding FindingGroundingGate }

// FindingsScopeKey resolves the root session for a finding.
type FindingsScopeKey func(ctx context.Context, sessionID string) string

func FindingsHandler(gates RecordFindingGates, store findings.Store, scopeKey FindingsScopeKey) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		rootSession := strings.TrimSpace(scopeKey(ctx, tctx.SessionID))
		if rootSession == "" {
			return "", fmt.Errorf("session required")
		}
		summary, _ := args["summary"].(string)
		summary = strings.TrimSpace(summary)
		if summary == "" {
			return "", fmt.Errorf("summary is required")
		}
		body, _ := args["body"].(string)
		if utf8.RuneCountInString(summary) > findings.SummaryMaxChars {
			return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "summary", "reason": "summary_exceeds_320_characters", "maximum": findings.SummaryMaxChars, "actual": utf8.RuneCountInString(summary), "unit": "characters"}}
		}
		if len(body) > findings.BodyMaxBytes {
			return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "body", "reason": "body_exceeds_8192_bytes", "maximum": findings.BodyMaxBytes, "actual": len(body), "unit": "bytes"}}
		}
		ref, _ := args["ref"].(string)
		ref = strings.TrimSpace(ref)
		if len(ref) > 512 {
			return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "ref", "reason": "ref_exceeds_512_bytes", "maximum": 512, "unit": "bytes"}}
		}
		if ref == "" {
			return "", &tools.ToolReject{Code: "FINDING_UNGROUNDED", Data: map[string]any{"reason": "reference_required"}}
		}

		agent := strings.TrimSpace(tctx.WorkerJobID)
		if agent == "" {
			agent = strings.TrimSpace(tctx.Agent)
		}
		if agent == "" {
			agent = findingAgentCoordinator
		}

		if gates.Grounding != nil {
			if err := gates.Grounding.AuditFinding(ctx, summary, ref, tctx); err != nil {
				return "", err
			}
		}
		status := "unchanged"
		appended, err := store.Append(ctx, rootSession, agent, summary, ref, body)
		if err != nil {
			return "", fmt.Errorf("store finding: %w", err)
		}
		if appended {
			status = "appended"
			findings.NotifyAppendObservers(ctx, findings.AppendEvent{SessionID: rootSession})
		}

		raw, _ := surveyjson.Marshal(map[string]any{"status": status, "kind": "note"})
		return string(raw), nil
	}
}
