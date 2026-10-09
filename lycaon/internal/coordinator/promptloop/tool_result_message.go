package promptloop

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/datamark"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *toolInvocations) composeToolResultMessage(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	call api.ToolCall,
	assistantMessageID string,
	origin api.MessageOrigin,
	run *toolInvocation,
) api.Message {
	result := guidance.ComposeToolResult(run.content, run.facts, l.Closeout.Deps.HintConfig)
	if result != nil {
		result.Tool = call.Name
		result.ToolCallID = call.ID
		result.AssistantMessageID = strings.TrimSpace(assistantMessageID)
		result.ToolArgs = call.Args
		rootSessionID := ""
		if l.Context.Deps.RootSessionID != nil {
			rootSessionID = l.Context.Deps.RootSessionID(ctx, sessionID)
		}
		projectID := ""
		if sess != nil {
			projectID = sess.ProjectID
		}
		run.content = applyToolResultSidecars(ctx, l.Deps.VisualStore, l.Deps.DesignateProjectCover, projectID, rootSessionID, sessionID, call.Name, run.content, result, run.captures)
	}
	// Metadata can rewrite content, so provenance framing comes last.
	content := run.content
	if api.ExternallyAuthored(origin) && !datamark.Framed(content) {
		content = datamark.Frame(retrievalSourceLabel(call.Name, run.captures.retrievedFrom), content, time.Now().UTC())
	}
	if result != nil {
		result.Content = content
	}
	return api.Message{
		SourceContext: run.captures.sourceContext,
		ID:            uuid.NewString(),
		Role:          api.MessageRoleTool,
		Origin:        origin,
		Authority:     api.ContentAuthorityNone,
		TrustTier:     api.ContentTrustTierUntrusted,
		Content:       content,
		ToolResult:    result,
	}
}

var spillWireLog = observability.LazyComponent("tool_spill")

// truncateToolResultForSession fits screened tool output to the session wire.
func (l *toolInvocations) truncateToolResultForSession(
	ctx context.Context,
	tool string,
	projection toolResultStorageProjection,
	rawContent string,
	maxBytes, maxSpillBytes int,
	sess *api.Session,
) toolResultProjection {
	hostDataDir := ""
	if sess != nil {
		hostDataDir = project.HostDataDir(l.Deps.DataDir, sess.ProjectID)
		if hostDataDir != "" {
			_, _ = project.EnsureHostDataDir(l.Deps.DataDir, sess.ProjectID)
		}
	}
	durableContent := projection.content
	limit := agentpresence.OutputLimit{}
	if tool == "git_diff" {
		fitted, reject := projectGitDiff(hostDataDir, tooloutput.Screened(durableContent), maxSpillBytes)
		if reject != nil {
			return toolResultProjection{reject: reject}
		}
		if fitted != durableContent {
			limit.Spilled = true
			rawContent = fitted
			durableContent = fitted
		}
	}
	// Clamp after redaction so truncation cannot split a matched secret.
	if clamp, ok := surveyreceipt.ClampSessionToolOutput(durableContent, maxBytes); ok {
		msg := guidance.EnvelopeHintMessage(ctx, l.Closeout.Deps.HintConfig, "TOOL_SURVEY_BYTE_CLAMPED", clamp.Vars)
		return toolResultProjection{
			content: guidance.AppendOutputBanner(clamp.Output, "TOOL_SURVEY_BYTE_CLAMPED", msg),
			limit:   agentpresence.OutputLimit{KeptEntries: clamp.KeptEntries, KeptThroughLine: clamp.KeptThroughLine},
		}.withCode("TOOL_SURVEY_BYTE_CLAMPED")
	}
	overlayPromote := tooloutput.IsOverlayPromoteTool(tool)
	if overlayPromote {
		maxBytes = tooloutput.OverlayToolResultMaxBytes(maxBytes)
	}
	screened := tooloutput.Screened(durableContent)
	inline := screened
	if overlayPromote {
		if compact, ok := tooloutput.InlineOverlayPromoteJSON(durableContent); ok {
			inline = tooloutput.Screened(compact)
		}
	}

	var out tooloutput.WireSpillOutcome
	if overlayPromote && hostDataDir != "" {
		rel := tooloutput.PromoteSpillRelPath(extractOverlayIDFromToolOutput(durableContent))
		out = tooloutput.WireSpillOverlayPromote(hostDataDir, rel, screened, inline, maxBytes, maxSpillBytes)
	} else {
		out = tooloutput.WireSpillToolOutput(hostDataDir, screened, maxBytes, maxSpillBytes)
	}
	if out.RejectCode != "" {
		rejectData := out.RejectData
		if out.RejectCode == tooloutput.ToolOutputSpillCapExceededCode {
			rejectData = tooloutput.EnrichSpillCapReject(tool, projection.args, durableContent, maxSpillBytes)
		}
		spillWireLog.Info("tool spill rejected at commit",
			"code", out.RejectCode,
			"tool", tool,
			"original_bytes", out.OriginalBytes,
			"reason", rejectData["reason"])
		return toolResultProjection{reject: &toolrejection.ToolReject{Code: out.RejectCode, Data: rejectData}}
	}
	if !out.Truncated {
		// Uncut output preserves the original one-request overlay.
		if maxBytes <= 0 || len(rawContent) <= maxBytes {
			return toolResultProjection{content: rawContent, limit: limit}
		}
		return toolResultProjection{content: out.Preview, limit: limit}
	}
	if out.SpillCapped {
		spillWireLog.Info("tool spill capped",
			"tool", tool,
			"original_bytes", out.OriginalBytes,
			"spill_bytes", out.SpillBytes,
			"spill_path", out.SpillPath)
	} else if out.SpillPath != "" {
		spillWireLog.Info("tool spill written",
			"tool", tool,
			"spill_bytes", out.SpillBytes,
			"spill_path", out.SpillPath)
	}
	hintVars := spillHintVars(out)
	if overlayPromote {
		hintVars["job_id"] = extractOverlayIDFromToolOutput(durableContent)
		hint := guidance.EnvelopeHintMessage(ctx, l.Closeout.Deps.HintConfig, "OVERLAY_PROMOTE_SPILL", hintVars)
		return toolResultProjection{
			content: guidance.AppendOutputBanner(out.Preview, "OVERLAY_PROMOTE_SPILL", hint),
			limit:   agentpresence.OutputLimit{Spilled: true},
		}.withCode("OVERLAY_PROMOTE_SPILL")
	}
	hintCode := "TOOL_OUTPUT_TRUNCATED"
	if out.SpillCapped {
		hintCode = "TOOL_OUTPUT_SPILL_CAPPED"
		hintVars["cap"] = tooloutput.EffectiveMaxSpillFileBytes(maxSpillBytes)
	}

	hint := guidance.EnvelopeHintMessage(ctx, l.Closeout.Deps.HintConfig, hintCode, hintVars)
	return toolResultProjection{
		content: guidance.AppendOutputBanner(out.Preview, hintCode, hint),
		limit:   agentpresence.OutputLimit{Spilled: true},
	}.withCode(hintCode)
}

func spillHintVars(out tooloutput.WireSpillOutcome) map[string]any {
	return map[string]any{
		"spill_path":     out.SpillPath,
		"spill_bytes":    out.SpillBytes,
		"original_bytes": out.OriginalBytes,
		"max_file_bytes": readcaps.MaxFileBytes,
		"spill_readable": out.SpillPath != "" && out.SpillBytes <= readcaps.MaxFileBytes,
	}
}

func extractOverlayIDFromToolOutput(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	for i := len(content) - 1; i >= 0; i-- {
		if content[i] != '{' {
			continue
		}
		var payload struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal([]byte(content[i:]), &payload); err != nil {
			continue
		}
		if id := strings.TrimSpace(payload.JobID); id != "" {
			return id
		}
	}
	return ""
}

func (l *toolInvocations) reloadHistoryAfterToolCompaction(ctx context.Context, sessionID string, sess *api.Session, surfaceID string, history []api.Message, st *promptLoopTurnState) ([]api.Message, error) {
	if l.Deps.CompactOversizedToolResults != nil {
		if err := l.Deps.CompactOversizedToolResults(ctx, sessionID, sess); err != nil {
			return history, err
		}
	}
	if l.Deps.ReloadHistory == nil {
		return history, nil
	}
	reloaded, err := l.Deps.ReloadHistory(ctx, sessionID, sess, surfaceID)
	if err != nil {
		return history, err
	}
	return st.applySecretStorageOverlays(reloaded), nil
}
