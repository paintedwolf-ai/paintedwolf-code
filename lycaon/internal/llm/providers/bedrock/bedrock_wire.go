package bedrock

import (
	"encoding/base64"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectMessages projects host messages onto Converse. Only the preamble
// becomes standing system content; later system rows are user text so host
// state stays in conversation order. Each checkpoint follows the last block
// its marked row emits, or the system prompt when the row is part of it.
func ProjectMessages(msgs []api.Message, checkpoints []providerwire.PromptCacheBreakpoint, vision bool, sessionID string) (system []brtypes.SystemContentBlock, out []brtypes.Message) {
	var sys strings.Builder
	var systemCheckpoint *providerwire.PromptCacheBreakpoint
	marked := providerwire.PromptCacheBreakpointSet(checkpoints)
	preambleEnd := providerwire.SystemPreambleEnd(msgs)
	pendingToolUseIDs := make(providerwire.ToolCallPairs)

	add := func(role brtypes.ConversationRole, block brtypes.ContentBlock) {
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, block)
			return
		}
		out = append(out, brtypes.Message{Role: role, Content: []brtypes.ContentBlock{block}})
	}

	checkpoint := func(bp providerwire.PromptCacheBreakpoint) {
		if len(out) == 0 {
			if sys.Len() > 0 {
				systemCheckpoint = &bp
			}
			return
		}
		last := &out[len(out)-1]
		if _, dup := last.Content[len(last.Content)-1].(*brtypes.ContentBlockMemberCachePoint); dup {
			return
		}
		last.Content = append(last.Content, &brtypes.ContentBlockMemberCachePoint{Value: cachePoint(bp)})
	}

	for i, m := range msgs {
		if bp, ok := marked[i-1]; i > 0 && ok {
			checkpoint(bp)
		}
		switch m.Role {
		case api.MessageRoleSystem:
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			if i >= preambleEnd {
				add(brtypes.ConversationRoleUser, &brtypes.ContentBlockMemberText{Value: m.Content})
				continue
			}
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(m.Content)
		case api.MessageRoleAssistant:
			if m.Content != "" {
				add(brtypes.ConversationRoleAssistant, &brtypes.ContentBlockMemberText{Value: m.Content})
			}
			if len(m.ToolCalls) > 0 {
				clear(pendingToolUseIDs)
				for _, tc := range m.ToolCalls {
					id := pendingToolUseIDs.Add(tc, true)
					add(brtypes.ConversationRoleAssistant, &brtypes.ContentBlockMemberToolUse{
						Value: brtypes.ToolUseBlock{
							ToolUseId: aws.String(id),
							Name:      aws.String(tc.Name),
							Input:     document.NewLazyDocument(providerwire.ToolArgs(tc.Args)),
						},
					})
				}
			}
		case api.MessageRoleTool:
			content := m.Content
			if content == "" && m.ToolResult != nil {
				content = m.ToolResult.Content
			}
			toolUseID, paired := pendingToolUseIDs.Take(m.ToolResult)
			if !paired {
				continue
			}
			result := []brtypes.ToolResultContentBlock{&brtypes.ToolResultContentBlockMemberText{Value: content}}
			if image, ok := bedrockToolResultImage(m); ok {
				result = append(result, &brtypes.ToolResultContentBlockMemberImage{Value: image})
			}
			add(brtypes.ConversationRoleUser, &brtypes.ContentBlockMemberToolResult{
				Value: brtypes.ToolResultBlock{ToolUseId: aws.String(toolUseID), Content: result},
			})
		default:
			if m.Content != "" {
				add(brtypes.ConversationRoleUser, &brtypes.ContentBlockMemberText{Value: m.Content})
			}
			if vision {
				for _, artifactID := range m.ArtifactIDs {
					mime, encoded, ok := providerwire.UserArtifactWireImage(sessionID, artifactID)
					if !ok {
						continue
					}
					data, err := base64.StdEncoding.DecodeString(encoded)
					if err != nil {
						continue
					}
					format, ok := bedrockImageFormat(mime)
					if !ok {
						continue
					}
					add(brtypes.ConversationRoleUser, &brtypes.ContentBlockMemberImage{Value: brtypes.ImageBlock{
						Format: format, Source: &brtypes.ImageSourceMemberBytes{Value: data},
					}})
				}
			}
		}
	}
	if bp, ok := marked[len(msgs)-1]; len(msgs) > 0 && ok {
		checkpoint(bp)
	}
	if sys.Len() > 0 {
		system = []brtypes.SystemContentBlock{&brtypes.SystemContentBlockMemberText{Value: sys.String()}}
		if systemCheckpoint != nil {
			system = append(system, &brtypes.SystemContentBlockMemberCachePoint{Value: cachePoint(*systemCheckpoint)})
		}
	}
	return system, out
}

// cachePoint is one checkpoint with the lifetime its boundary takes.
func cachePoint(bp providerwire.PromptCacheBreakpoint) brtypes.CachePointBlock {
	return brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault, Ttl: brtypes.CacheTTL(providerwire.LifetimeToken(bp.Lifetime))}
}

// bedrockToolResultImage converts the image a prepared tool result attaches.
func bedrockToolResultImage(m api.Message) (brtypes.ImageBlock, bool) {
	image, ok := providerwire.ToolResultImage(m)
	if !ok {
		return brtypes.ImageBlock{}, false
	}
	format, ok := bedrockImageFormat(image.Mime)
	if !ok {
		return brtypes.ImageBlock{}, false
	}
	data, err := base64.StdEncoding.DecodeString(image.Base64)
	if err != nil {
		return brtypes.ImageBlock{}, false
	}
	return brtypes.ImageBlock{Format: format, Source: &brtypes.ImageSourceMemberBytes{Value: data}}, true
}

func bedrockImageFormat(mime string) (brtypes.ImageFormat, bool) {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return brtypes.ImageFormatPng, true
	case "image/jpeg":
		return brtypes.ImageFormatJpeg, true
	case "image/gif":
		return brtypes.ImageFormatGif, true
	case "image/webp":
		return brtypes.ImageFormatWebp, true
	default:
		return "", false
	}
}

// mapConverseOutput projects a Converse response onto a Completion.
func mapConverseOutput(out *bedrockruntime.ConverseOutput) *modelcall.Completion {
	if out == nil {
		return &modelcall.Completion{}
	}
	completion := &modelcall.Completion{Usage: bedrockUsage(out.Usage)}
	msg, ok := out.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok {
		return completion
	}
	var content strings.Builder
	for _, block := range msg.Value.Content {
		switch b := block.(type) {
		case *brtypes.ContentBlockMemberText:
			content.WriteString(b.Value)
		case *brtypes.ContentBlockMemberToolUse:
			completion.ToolCalls = append(completion.ToolCalls, api.ToolCall{
				ID:     providerwire.NewToolCallID(),
				WireID: aws.ToString(b.Value.ToolUseId),
				Name:   aws.ToString(b.Value.Name),
				Args:   documentToMap(b.Value.Input),
			})
		}
	}
	completion.Content = content.String()
	return completion
}

func bedrockUsage(u *brtypes.TokenUsage) modelcall.TokenUsage {
	if u == nil {
		return modelcall.TokenUsage{}
	}
	cacheRead := int(aws.ToInt32(u.CacheReadInputTokens))
	cacheWrite := int(aws.ToInt32(u.CacheWriteInputTokens))
	var cacheWrite1H int
	for _, detail := range u.CacheDetails {
		if detail.Ttl == brtypes.CacheTTLOneHour {
			cacheWrite1H += int(aws.ToInt32(detail.InputTokens))
		}
	}
	return modelcall.TokenUsage{
		Present:                    u.InputTokens != nil || u.OutputTokens != nil,
		Incomplete:                 u.InputTokens == nil || u.OutputTokens == nil,
		PromptTokens:               int(aws.ToInt32(u.InputTokens)) + cacheRead + cacheWrite,
		CompletionTokens:           int(aws.ToInt32(u.OutputTokens)),
		CacheReadInputTokens:       cacheRead,
		CacheCreationInputTokens:   cacheWrite,
		CacheCreation1HInputTokens: cacheWrite1H,
	}
}

func documentToMap(doc document.Interface) map[string]any {
	if doc == nil {
		return nil
	}
	var m map[string]any
	if err := doc.UnmarshalSmithyDocument(&m); err != nil {
		return nil
	}
	return m
}
