// Package promptassembly filters, pins, seals, and fits prepared transcript history for a model request.
package promptassembly

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

var assemblyLog = observability.LazyComponent("prompt_assembly")

// Config supplies the resolved policy for the history assembly pipeline.
type Config struct {
	CompactionViewApplied bool
	CompactionConfig      compaction.CompactionConfig
	// TokenCalibration maps provider token counts to host estimates.
	// A zero value uses ColdStartOverhead as an additive reserve.
	TokenCalibration compaction.PromptTokenCalibration
	// ColdStartOverhead is the additive reserve used until the first observation.
	ColdStartOverhead int
	SurfaceID         string
}

// Report describes prompt-history assembly steps applied for operator debug.
type Report struct {
	TokensBefore int
	TokensAfter  int
	Strategies   []string
}

// CompactionReport maps assembly token deltas into the compaction report shape.
func (r Report) CompactionReport() compaction.CompactionReport {
	return compaction.CompactionReport{
		TokensBefore: r.TokensBefore,
		TokensAfter:  r.TokensAfter,
	}
}

// Assemble filters, seals, and fits a prepared transcript without storing it.
func Assemble(sess *api.Session, history []api.Message, deps Config) ([]api.Message, Report) {
	if len(history) == 0 && !deps.CompactionViewApplied {
		return history, Report{}
	}
	out := history
	var strategies []string
	if deps.CompactionViewApplied {
		strategies = append(strategies, "compaction_view")
	}
	// After the compaction view so its ordinal splice stays on canonical history.
	out = api.FilterPromptHistory(out)
	strategies = append(strategies, "filter:prompt_history")
	// Pin before any stage that can drop rows, so they all see one protected set.
	out = MarkContextPins(sess, out)

	report := Report{
		TokensBefore: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(out)),
		Strategies:   strategies,
	}
	if deps.SurfaceID == surface.SurfaceImplementOverlayPromote {
		out = compaction.DietOverlayPromoteMessages(out)
		report.Strategies = append(report.Strategies, "surface:overlay_promote")
	}
	sealed := sealToolRoleBodies(out)
	report.Strategies = append(report.Strategies, "seal:tool_bodies")
	if deps.CompactionConfig.Enabled && deps.CompactionConfig.HardCeilingTokens > 0 {
		ctxMsgs := compaction.ContextMessagesFromAPI(out)
		maxTokens := compaction.FitMaxTokens(deps.CompactionConfig, deps.TokenCalibration, deps.ColdStartOverhead)
		fitted := compaction.DeterministicFit(deps.CompactionConfig, ctxMsgs, maxTokens)
		out = compaction.ContextMessagesToAPI(fitted, out)
		report.Strategies = append(report.Strategies, "fit:hard_ceiling")
	}
	out = restoreSealedToolBodies(out, sealed)
	// Filter again: membership restore can reattach chrome Kind from a stale view.
	out = api.FilterPromptHistory(out)
	report.TokensAfter = compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(out))
	if len(report.Strategies) > 0 {
		assemblyLog.Debug("prompt history assembled",
			"session_id", sessionID(sess),
			"tokens_before", report.TokensBefore,
			"tokens_after", report.TokensAfter,
			"strategies", report.Strategies,
		)
	}
	return out, report
}

func sessionID(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID
}

// MarkContextPins preserves the current request and worker charter during fitting.
func MarkContextPins(sess *api.Session, messages []api.Message) []api.Message {
	if sess == nil || len(messages) == 0 {
		return messages
	}
	latestIntent := api.UserIntentBoundary(messages) - 1
	out := messages
	copied := false
	for i := range messages {
		if len(messages[i].ToolCalls) > 0 || messages[i].ToolResult != nil {
			continue
		}
		if i != latestIntent && !(sess.IsWorkerChild() && pinnableCharterRow(messages[i])) {
			continue
		}
		if !copied {
			out = append([]api.Message(nil), messages...)
			copied = true
		}
		out[i].ContextPinned = true
	}
	return out
}

// pinnableCharterRow identifies standalone worker charter rows.
func pinnableCharterRow(msg api.Message) bool {
	if len(msg.ToolCalls) > 0 || msg.ToolResult != nil {
		return false
	}
	switch msg.Role {
	case api.MessageRoleUser, api.MessageRoleSystem:
	default:
		return false
	}
	return guidance.CarriesWorkerCharter(msg.Content)
}

// sealToolRoleBodies snapshots tool-role Content after durable and surface projections.
func sealToolRoleBodies(msgs []api.Message) map[string]string {
	if len(msgs) == 0 {
		return nil
	}
	out := make(map[string]string, len(msgs))
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		out[id] = msg.Content
	}
	return out
}

// restoreSealedToolBodies writes sealed Content back onto surviving tool rows.
func restoreSealedToolBodies(msgs []api.Message, sealed map[string]string) []api.Message {
	if len(msgs) == 0 || len(sealed) == 0 {
		return msgs
	}
	for i := range msgs {
		if msgs[i].Role != api.MessageRoleTool {
			continue
		}
		id := strings.TrimSpace(msgs[i].ID)
		if id == "" {
			continue
		}
		body, ok := sealed[id]
		if !ok {
			continue
		}
		msgs[i].Content = body
		if msgs[i].ToolResult != nil {
			tr := *msgs[i].ToolResult
			tr.Content = body
			msgs[i].ToolResult = &tr
		}
	}
	return msgs
}
