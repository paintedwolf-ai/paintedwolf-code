package compaction

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

type CompactionInput struct {
	Text     string
	Messages []api.Message
}

// BuildCompactionInput fits the prompt and its source provenance together.
func BuildCompactionInput(
	ctx context.Context,
	info SessionInfo,
	sliced []ContextMessage,
	maxTokens int,
	maxMessageTokens int,
) (CompactionInput, error) {
	if maxTokens <= 0 || maxMessageTokens <= 0 {
		return CompactionInput{}, fmt.Errorf("compaction resume prompt requires positive token budgets")
	}
	rows, messages := compactionRecentMessages(sliced, maxMessageTokens)
	for {
		block, err := renderCompactionUserPrompt(ctx, info, rows)
		if err != nil {
			return CompactionInput{}, err
		}
		if tokenest.EstimateDefault(block) <= maxTokens {
			return CompactionInput{Text: block, Messages: messages}, nil
		}
		if len(rows) <= 1 {
			return CompactionInput{}, fmt.Errorf("compaction resume prompt fixed envelope exceeds %d tokens", maxTokens)
		}
		rows = rows[1:]
		messages = messages[1:]
	}
}

func renderCompactionUserPrompt(ctx context.Context, info SessionInfo, rows []map[string]any) (string, error) {
	data := map[string]any{
		"session_id":      strings.TrimSpace(info.ID),
		"posture":         info.Posture,
		"recent_messages": rows,
	}
	block, err := guidance.RenderGuidance(ctx, "compaction/resume-user", data)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", fmt.Errorf("compaction resume prompt rendered empty")
	}
	return block, nil
}

func compactionRecentMessages(sliced []ContextMessage, maxMessageTokens int) ([]map[string]any, []api.Message) {
	out := make([]map[string]any, 0, len(sliced))
	messages := make([]api.Message, 0, len(sliced))
	maxRunes := maxMessageTokens * tokenest.DefaultDivisor
	for _, msg := range sliced {
		content, origin, authority, trustTier := projectCompactionMessage(msg)
		content = runeclamp.Fit(content, maxRunes)
		messages = append(messages, api.Message{Content: content, SourceContext: msg.SourceContext})
		out = append(out, map[string]any{
			"role":                   msg.Role,
			"origin":                 origin,
			"authority":              authority,
			"trust_tier":             trustTier,
			"content":                content,
			"evidence_handles":       append([]string(nil), msg.EvidenceHandles...),
			"compaction_strategy":    compactionStrategy(msg),
			"host_secret_redaction":  msg.HostSecretRedaction.Redactions() > 0,
			"host_secret_references": msg.HostSecretRedaction.References() > 0,
		})
	}
	return out, messages
}

func compactionStrategy(msg ContextMessage) string {
	if msg.CompactedChunk == nil {
		return ""
	}
	return msg.CompactedChunk.Strategy
}

func projectCompactionMessage(msg ContextMessage) (string, string, string, string) {
	canonical := api.NormalizeMessageProvenance(ContextMessageToAPI(msg))
	if len(canonical.ContentParts) == 0 {
		content := transcript.ProjectContentParts(canonical.Role, api.MessageTextParts(canonical))
		return content, string(canonical.Origin), string(canonical.Authority), string(canonical.TrustTier)
	}

	origin := string(canonical.ContentParts[0].Origin)
	authority := string(canonical.ContentParts[0].Authority)
	trustTier := string(canonical.ContentParts[0].TrustTier)
	for _, part := range canonical.ContentParts[1:] {
		if string(part.Origin) != origin {
			origin = "mixed"
		}
		if string(part.Authority) != authority {
			authority = "mixed"
		}
		if string(part.TrustTier) != trustTier {
			trustTier = "mixed"
		}
	}
	return transcript.ProjectContentParts(canonical.Role, canonical.ContentParts), origin, authority, trustTier
}

// SliceMessages returns the last n messages.
func SliceMessages(messages []ContextMessage, n int) []ContextMessage {
	if n <= 0 || len(messages) <= n {
		out := make([]ContextMessage, len(messages))
		copy(out, messages)
		return out
	}
	out := make([]ContextMessage, n)
	copy(out, messages[len(messages)-n:])
	return out
}
