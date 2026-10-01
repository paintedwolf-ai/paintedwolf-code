package oar

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/guidance"
)

// RejectFormatter is the guidance formatter surface the renderer needs.
// Implemented by *guidance.StaticRejectFormatter.
type RejectFormatter interface {
	Format(code string, data map[string]any) (string, error)
}

// NudgeFormatter renders post-turn coordinator nudge copy (FormatCoordinatorNudge).
type NudgeFormatter interface {
	FormatNudge(ctx context.Context, code string, data map[string]any) (string, error)
}

// RenderChannel names the existing host channel a Decision maps onto.
type RenderChannel string

const (
	ChannelToolReject     RenderChannel = "tool_reject"
	ChannelGroundingNudge RenderChannel = "grounding_nudge"
	ChannelGuidanceNudge  RenderChannel = "guidance_nudge"
	ChannelBanner         RenderChannel = "banner"
	ChannelNone           RenderChannel = "none"
)

// RenderedDecision is a Decision mapped onto a host feedback channel.
type RenderedDecision struct {
	Decision Decision
	Channel  RenderChannel
	Text     string
}

// Renderer maps OAR Decisions onto existing reject/kick/nudge/banner channels.
type Renderer struct {
	Reject RejectFormatter
	Nudge  NudgeFormatter
}

// NewRenderer builds a Decision renderer.
func NewRenderer(reject RejectFormatter, nudge NudgeFormatter) *Renderer {
	return &Renderer{Reject: reject, Nudge: nudge}
}

// Render maps a resolved decision onto channels. Nudge and warn walk
// Advisories ([OAR-EVAL-20]). post_turn blocks use grounding_nudge;
// pre_invoke/tool_handler/finalize use tool_reject.
func (r *Renderer) Render(ctx context.Context, stage Stage, decision *Decision) ([]RenderedDecision, error) {
	if decision == nil {
		return nil, nil
	}
	switch decision.Effect {
	case EffectNudge, EffectWarn:
		out := make([]RenderedDecision, 0, len(decision.Advisories))
		for _, a := range decision.Advisories {
			rd, err := r.renderOne(ctx, stage, Decision{
				Effect: decision.Effect,
				Code:   a.Code,
				Rule:   a.Rule,
				Copy:   a.Copy,
				Data:   a.Data,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, rd)
		}
		return out, nil
	default:
		rd, err := r.renderOne(ctx, stage, *decision)
		if err != nil {
			return nil, err
		}
		return []RenderedDecision{rd}, nil
	}
}

func (r *Renderer) renderOne(ctx context.Context, stage Stage, d Decision) (RenderedDecision, error) {
	rd := RenderedDecision{Decision: d, Channel: ChannelNone}
	// [OAR-COPY-1] Use the engine's frozen rendered copy, including an empty one.
	if d.Copy != nil {
		switch d.Effect {
		case EffectBlock:
			rd.Channel = ChannelToolReject
			if stage == StagePostTurn {
				rd.Channel = ChannelGroundingNudge
			}
		case EffectWarn:
			rd.Channel = ChannelBanner
		case EffectNudge:
			rd.Channel = ChannelGuidanceNudge
		case EffectAllow, EffectTransform:
			return rd, nil
		default:
			return rd, fmt.Errorf("unknown effect %q", d.Effect)
		}
		text, err := guidance.RenderPolicyCopy(ctx, d.Code, string(d.Effect), d.Copy)
		rd.Text = text
		return rd, err
	}

	switch d.Effect {
	case EffectAllow:
		return rd, nil
	case EffectBlock, EffectTransform:
		if stage == StagePostTurn {
			rd.Channel = ChannelGroundingNudge
			if r != nil && r.Nudge != nil {
				text, err := r.Nudge.FormatNudge(ctx, d.Code, d.Data)
				if err != nil {
					return rd, fmt.Errorf("nudge format %s: %w", d.Code, err)
				}
				rd.Text = text
			}
			return rd, nil
		}
		rd.Channel = ChannelToolReject
		if r != nil && r.Reject != nil {
			text, err := r.Reject.Format(d.Code, d.Data)
			if err != nil {
				return rd, fmt.Errorf("reject format %s: %w", d.Code, err)
			}
			rd.Text = text
		}
		return rd, nil
	case EffectNudge:
		rd.Channel = ChannelGuidanceNudge
		if r != nil && r.Nudge != nil {
			text, err := r.Nudge.FormatNudge(ctx, d.Code, d.Data)
			if err != nil {
				return rd, fmt.Errorf("nudge format %s: %w", d.Code, err)
			}
			rd.Text = text
		}
		return rd, nil
	case EffectWarn:
		rd.Channel = ChannelBanner
		if r != nil && r.Reject != nil {
			text, err := r.Reject.Format(d.Code, d.Data)
			if err != nil {
				return rd, fmt.Errorf("banner format %s: %w", d.Code, err)
			}
			rd.Text = text
		}
		return rd, nil
	default:
		return rd, fmt.Errorf("unknown effect %q", d.Effect)
	}
}

// FirstBlock returns the first tool_reject / grounding_nudge render, if any.
func FirstBlock(rendered []RenderedDecision) (RenderedDecision, bool) {
	for _, rd := range rendered {
		if rd.Channel == ChannelToolReject || rd.Channel == ChannelGroundingNudge {
			return rd, true
		}
	}
	return RenderedDecision{}, false
}
