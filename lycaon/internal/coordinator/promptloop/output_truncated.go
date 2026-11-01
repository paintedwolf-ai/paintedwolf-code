package promptloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/pkg/api"
)

// maxOutputTruncatedRetries allows one in-turn recovery when a reply stops on
// an output token limit with no usable content.
const maxOutputTruncatedRetries = 1

// outputTruncatedRecoverySignalID identifies the in-turn host recovery nudge.
const outputTruncatedRecoverySignalID = "output_truncated_recovery"

type modelEntryLookupClient interface {
	ModelEntry(model string) (modelinfo.Entry, bool)
}

func (l *modelTurn) modelEntryForTurn(ctx context.Context, providerID, model string) modelinfo.Entry {
	if l == nil {
		return modelinfo.Entry{}
	}
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil {
		if entry, ok := llm.LookupModelEntry(ctx, l.Deps.LLMService.Registry, providerID, model); ok {
			return entry
		}
	}
	if lookup, ok := l.Deps.LLM.(modelEntryLookupClient); ok {
		if entry, found := lookup.ModelEntry(model); found {
			return entry
		}
	}
	return modelinfo.Entry{}
}

func (l *turnNudges) handleStreamTurnError(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	err error,
	completion *modelcall.Completion,
	st *promptLoopTurnState,
) (retry bool, exit loopExitKind, outErr error) {
	if errors.Is(err, ErrLLMTurnTimeout) && st != nil && strings.TrimSpace(st.lastAssistantID) != "" {
		return false, loopExitLLMTimeout, nil
	}
	if errors.Is(err, llm.ErrModelRequestSecretWithheld) {
		retry, werr := l.handleSecretWithheldTurn(ctx, sess, sessionID, err, st)
		if werr != nil {
			return false, loopExitNone, werr
		}
		if retry {
			return true, loopExitNone, nil
		}
		return false, loopExitSecretWithheld, nil
	}
	var outputTruncated *failure.ProviderOutputTruncatedError
	if errors.As(err, &outputTruncated) {
		retry, terr := l.handleOutputTruncatedTurn(ctx, sess, sessionID, outputTruncated, completion, st)
		if terr != nil {
			return false, loopExitNone, terr
		}
		if retry {
			return true, loopExitNone, nil
		}
		return false, loopExitOutputTruncated, nil
	}
	return false, loopExitNone, err
}

// handleOutputTruncatedTurn drops a length-truncated reply, appends an in-turn
// host recovery notice once, and ends the turn with a typed outcome and a
// visible notice if the recovery attempt also hits the output limit.
func (l *turnNudges) handleOutputTruncatedTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	truncated *failure.ProviderOutputTruncatedError,
	completion *modelcall.Completion,
	st *promptLoopTurnState,
) (retry bool, err error) {
	if st == nil || truncated == nil {
		return false, truncated
	}
	var entry modelinfo.Entry
	if l != nil && l.Model != nil {
		entry = l.Model.modelEntryForTurn(ctx, truncated.ProviderID, truncated.Model)
	}
	truncated = modelcall.EnrichOutputTruncatedError(truncated, modelcall.CompletionRequest{}, entry, completion)

	if st.draftSlotAppended {
		if withdrawErr := l.withdrawCoordinatorDraft(context.WithoutCancel(ctx), sess, sessionID, st, ""); withdrawErr != nil {
			return false, withdrawErr
		}
	} else {
		st.draftSlotID = ""
	}

	if st.output.truncatedRetries < maxOutputTruncatedRetries {
		st.output.truncatedRetries++
		msg := buildOutputTruncatedRecoveryMessage(truncated)
		st.history = append(st.history, msg)
		if deferUserNudgeStore(st) {
			st.deferredUserNudges = append(st.deferredUserNudges, msg)
			return true, nil
		}
		if l == nil || l.Projection == nil || l.Projection.Deps.AppendMessages == nil {
			return false, fmt.Errorf("append messages not configured")
		}
		if err := l.Projection.Deps.AppendMessages(ctx, sessionID, msg); err != nil {
			return false, err
		}
		return true, nil
	}

	truncated.RecoveryAttempted = true
	if truncated.Attempts < st.output.truncatedRetries+1 {
		truncated.Attempts = st.output.truncatedRetries + 1
	}
	msg := buildOutputTruncatedTerminalMessage(truncated)
	if l == nil || l.Projection == nil || l.Projection.Deps.AppendMessages == nil {
		return false, fmt.Errorf("append messages not configured")
	}
	if err := l.Projection.Deps.AppendMessages(ctx, sessionID, msg); err != nil {
		return false, err
	}
	if l.Projection.Deps.Streams != nil {
		l.Projection.Deps.Streams.CacheReplay(msg.ID, msg.Content, nil)
	}
	st.history = append(st.history, msg)
	st.lastAssistantID = msg.ID
	st.lastAssistantContent = msg.Content
	st.output.id = ""
	st.proseFinishDelivered = true
	st.output.truncatedOutcome = &promptresult.OutputTruncatedOutcome{
		ProviderID:          strings.TrimSpace(truncated.ProviderID),
		Model:               strings.TrimSpace(truncated.Model),
		FinishReason:        strings.TrimSpace(truncated.FinishReason),
		MaxTokens:           truncated.MaxTokens,
		ConfiguredMaxTokens: truncated.ConfiguredMaxTokens,
		ProviderMaxTokens:   truncated.ProviderMaxTokens,
		LowOutputCeiling:    truncated.LowOutputCeiling(),
		ToolCallTruncated:   truncated.ToolCallTruncated,
		ToolName:            strings.TrimSpace(truncated.ToolName),
		ToolArgsBytes:       truncated.ToolArgsBytes,
		Notice:              msg.Content,
	}
	return false, nil
}

func buildOutputTruncatedRecoveryMessage(truncated *failure.ProviderOutputTruncatedError) api.Message {
	instruction := "Host notice: your previous reply hit the model output token limit before producing a usable response and was dropped. Split large writes into smaller steps or shorten your reply."
	data := formatOutputTruncatedHostData(truncated)
	content := instruction + "\n\n" + data
	return api.Message{
		ID:           uuid.NewString(),
		Role:         api.MessageRoleUser,
		Content:      content,
		Origin:       api.MessageOriginHost,
		Authority:    api.ContentAuthoritySystem,
		TrustTier:    api.ContentTrustTierTrusted,
		Kind:         api.MessageKindHostNudge,
		HostSignalID: outputTruncatedRecoverySignalID,
		Visibility:   api.MessageVisibilityInternal,
		CreatedAt:    time.Now().UTC(),
		ContentParts: []api.MessageContentPart{
			{
				Content:   instruction,
				Origin:    api.MessageOriginHost,
				Authority: api.ContentAuthoritySystem,
				TrustTier: api.ContentTrustTierTrusted,
			},
			{
				Content:   data,
				Origin:    api.MessageOriginHost,
				Authority: api.ContentAuthorityNone,
				TrustTier: api.ContentTrustTierTrusted,
				Source:    "output_truncated",
			},
		},
	}
}

func buildOutputTruncatedTerminalMessage(truncated *failure.ProviderOutputTruncatedError) api.Message {
	summary := formatOutputTruncatedTerminalSummary(truncated)
	data := formatOutputTruncatedHostData(truncated)
	content := summary + "\n\n" + data
	return api.Message{
		ID:         uuid.NewString(),
		Role:       api.MessageRoleAssistant,
		Content:    content,
		Origin:     api.MessageOriginHost,
		Authority:  api.ContentAuthoritySystem,
		TrustTier:  api.ContentTrustTierTrusted,
		Visibility: api.MessageVisibilityTranscript,
		CreatedAt:  time.Now().UTC(),
		ContentParts: []api.MessageContentPart{
			{
				Content:   summary,
				Origin:    api.MessageOriginHost,
				Authority: api.ContentAuthoritySystem,
				TrustTier: api.ContentTrustTierTrusted,
			},
			{
				Content:   data,
				Origin:    api.MessageOriginHost,
				Authority: api.ContentAuthorityNone,
				TrustTier: api.ContentTrustTierTrusted,
				Source:    "output_truncated",
			},
		},
	}
}

func formatOutputTruncatedTerminalSummary(truncated *failure.ProviderOutputTruncatedError) string {
	var b strings.Builder
	b.WriteString("The model reply reached the output token limit twice in a row without producing usable output")
	if truncated != nil && truncated.ToolCallTruncated {
		if tool := strings.TrimSpace(truncated.ToolName); tool != "" {
			fmt.Fprintf(&b, " (truncated `%s` tool call", tool)
			if truncated.ToolArgsBytes > 0 {
				fmt.Fprintf(&b, ", %d bytes of arguments", truncated.ToolArgsBytes)
			}
			b.WriteString(")")
		} else {
			b.WriteString(" (truncated tool call)")
		}
	}
	if truncated != nil && truncated.MaxTokens > 0 {
		fmt.Fprintf(&b, " at output limit %d tokens", truncated.MaxTokens)
	}
	b.WriteString(".")
	if truncated != nil && truncated.LowOutputCeiling() {
		fmt.Fprintf(&b, " The configured model output ceiling (%d tokens) is below what the provider allows (%d tokens); raise max_tokens in the model configuration to allow larger replies.", truncated.ConfiguredMaxTokens, truncated.ProviderMaxTokens)
	}
	return b.String()
}

func formatOutputTruncatedHostData(truncated *failure.ProviderOutputTruncatedError) string {
	if truncated == nil {
		return "finish_reason: length"
	}
	lines := make([]string, 0, 6)
	reason := strings.TrimSpace(truncated.FinishReason)
	if reason == "" {
		reason = "length"
	}
	lines = append(lines, fmt.Sprintf("finish_reason: %s", reason))
	if truncated.MaxTokens > 0 {
		lines = append(lines, fmt.Sprintf("output_token_limit: %d", truncated.MaxTokens))
	}
	if truncated.ToolCallTruncated {
		lines = append(lines, "tool_call_truncated: true")
		if tool := strings.TrimSpace(truncated.ToolName); tool != "" {
			lines = append(lines, fmt.Sprintf("truncated_tool: %s", tool))
		}
		if truncated.ToolArgsBytes > 0 {
			lines = append(lines, fmt.Sprintf("truncated_tool_args_bytes: %d", truncated.ToolArgsBytes))
		}
	}
	if truncated.LowOutputCeiling() {
		lines = append(lines, fmt.Sprintf("configured_max_tokens: %d (provider allows %d; raise max_tokens in configuration)", truncated.ConfiguredMaxTokens, truncated.ProviderMaxTokens))
	}
	return strings.Join(lines, "\n")
}
