package llm

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/harmony"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecoverHarmonyCompletion parses declared transcript channels.
func RecoverHarmonyCompletion(c *modelcall.Completion, toolsOffered bool) *modelcall.Completion {
	if c == nil || strings.TrimSpace(c.Content) == "" {
		return c
	}
	segs, ok := harmony.ParseTranscript(c.Content)
	if !ok {
		return c
	}
	var calls []api.ToolCall
	var final, analysis strings.Builder
	for _, seg := range segs {
		switch seg.Channel {
		case harmony.ChannelCommentary:
			if strings.TrimSpace(seg.Tool) == "" {
				continue
			}
			calls = append(calls, api.ToolCall{
				ID:   fmt.Sprintf("harmony-%d", len(calls)+1),
				Name: seg.Tool,
				Args: seg.Args,
			})
		case harmony.ChannelFinal:
			if text := strings.TrimSpace(seg.Text); text != "" {
				if final.Len() > 0 {
					final.WriteByte('\n')
				}
				final.WriteString(text)
			}
		case harmony.ChannelAnalysis:
			if text := strings.TrimSpace(seg.Text); text != "" {
				if analysis.Len() > 0 {
					analysis.WriteByte('\n')
				}
				analysis.WriteString(text)
			}
		}
	}
	if analysis.Len() > 0 && strings.TrimSpace(c.Reasoning) == "" {
		c.Reasoning = analysis.String()
	}
	if toolsOffered && len(calls) > 0 {
		if len(c.ToolCalls) == 0 {
			c.ToolCalls = calls
		}
		c.Content = ""
		return c
	}
	if text := strings.TrimSpace(final.String()); text != "" {
		c.Content = text
		c.ToolCalls = nil
		return c
	}
	return c
}
