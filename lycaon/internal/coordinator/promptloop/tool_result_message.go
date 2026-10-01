package promptloop

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/datamark"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *PromptLoop) composeToolResultMessage(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	call api.ToolCall,
	assistantMessageID string,
	origin api.MessageOrigin,
	run *toolInvocation,
) api.Message {
	result := guidance.ComposeToolResult(run.content, run.facts, l.Deps.HintConfig)
	if result != nil {
		result.Tool = call.Name
		result.ToolCallID = call.ID
		result.AssistantMessageID = strings.TrimSpace(assistantMessageID)
		result.ToolArgs = call.Args
		rootSessionID := ""
		if l.Deps.RootSessionID != nil {
			rootSessionID = l.Deps.RootSessionID(ctx, sessionID)
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
