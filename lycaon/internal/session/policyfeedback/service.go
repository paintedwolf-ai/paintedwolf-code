package policyfeedback

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

type Service struct{ renderer *oar.Renderer }

func New() *Service                                   { return &Service{} }
func (m *Service) SetRenderer(renderer *oar.Renderer) { m.renderer = renderer }
func (m *Service) RenderResult(ctx context.Context, anchor string, res *oar.PipelineResult) (*guidance.Refusal, bool, error) {
	if res == nil || !res.Enforced || res.Decision == nil {
		return nil, false, nil
	}
	blocks := res.Decision.Effect == oar.EffectBlock
	var fallback *guidance.Refusal
	if blocks {
		fallback = guidance.NewRefusal(res.Decision.Code, res.Decision.Code).WithPolicyCopy(res.Decision.Copy).
			WithDetails(Details(res.Decision.Rule, res.Decision.Data), Subject("", res.Decision.Data))
	}
	if m.renderer == nil {
		return fallback, blocks, nil
	}
	rendered, rerr := m.renderer.Render(ctx, oar.StageFromAnchor(anchor), res.Decision)
	if rerr != nil {
		return nil, false, rerr
	}
	if rd, ok := oar.FirstBlock(rendered); ok && rd.Text != "" {
		return guidance.NewRefusal(rd.Decision.Code, rd.Text).WithPolicyCopy(rd.Decision.Copy).WithDetails(Details(rd.Decision.Rule, rd.Decision.Data), Subject("", rd.Decision.Data)), true, nil
	}
	var bodies []string
	for _, rd := range rendered {
		if (rd.Channel == oar.ChannelBanner || rd.Channel == oar.ChannelGuidanceNudge) && rd.Text != "" {
			bodies = append(bodies, rd.Text)
		}
	}
	if len(bodies) > 0 {
		ref := guidance.NewRefusal("", strings.Join(bodies, "\n")).WithPolicyCopy(res.Decision.Copy)
		if !blocks {
			ref.Facts.Outcome = api.ToolResultOutcomeCompleted
		}
		for _, a := range res.Decision.Advisories {
			ref.Facts = ref.Facts.WithFeedback(a.Code, Details(a.Rule, a.Data), Subject("", a.Data))
		}
		return ref, blocks, nil
	}
	return fallback, blocks, nil
}

func Details(rule string, data map[string]any) map[string]any {
	if rule == "" {
		return data
	}
	out := maps.Clone(data)
	if out == nil {
		out = map[string]any{}
	}
	out["policy_rule"] = rule
	return out
}
func Subject(sessionID string, data map[string]any) *api.FeedbackSubject {
	if raw, ok := data["subject"].(map[string]any); ok {
		kind, _ := raw["kind"].(string)
		id, _ := raw["id"].(string)
		if strings.TrimSpace(kind) != "" && strings.TrimSpace(id) != "" {
			return &api.FeedbackSubject{Kind: strings.TrimSpace(kind), ID: strings.TrimSpace(id)}
		}
	}
	if sessionID == "" {
		return nil
	}
	return &api.FeedbackSubject{Kind: "session", ID: sessionID}
}

func (m *Service) DeliverPostToolResult(ctx context.Context, anchor, output string, res *oar.PipelineResult) (string, guidance.ToolResultFacts) {
	facts := guidance.ToolResultFacts{}
	if res == nil || res.Decision == nil {
		return output, facts
	}
	decision := res.Decision
	if decision.Effect == oar.EffectNudge {
		return output, facts
	}
	if decision.Effect == oar.EffectTransform {
		facts.ContentReplaced = true
		return res.Content, facts.WithFeedback(decision.Code, Details(decision.Rule, decision.Data), Subject("", decision.Data))
	}
	banner, _, err := m.RenderResult(ctx, anchor, res)
	if decision.Effect == oar.EffectBlock {
		facts.ContentReplaced = true
		facts = facts.WithFeedback(decision.Code, Details(decision.Rule, decision.Data), Subject("", decision.Data)).WithOutcome(api.ToolResultOutcomeRejected)
		if err != nil || banner == nil {
			return "", facts
		}
		return banner.Body, facts
	}
	if err != nil || banner == nil || strings.TrimSpace(banner.Body) == "" {
		return output, facts
	}
	for _, advisory := range decision.Advisories {
		facts = facts.WithFeedback(advisory.Code, Details(advisory.Rule, advisory.Data), Subject("", advisory.Data))
	}
	return output + "\n" + banner.Body, facts.WithFeedback(decision.Code, Details(decision.Rule, decision.Data), Subject("", decision.Data))
}

func (m *Service) Renderer() *oar.Renderer { return m.renderer }
