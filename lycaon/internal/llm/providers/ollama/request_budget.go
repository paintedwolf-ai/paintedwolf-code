package ollama

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

// RequestBudget uses the same projection and calibration as native dispatch.
func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	req.AttemptBudget, req.ControlCapture = nil, nil
	model := p.ResolveModel(req)
	entry, _ := p.ModelEntry(model)
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	messages := ProjectMessages(req.Messages, MessageProjection{Vision: vision, SessionID: req.Debug.SessionID, ProviderID: p.id, Model: model})
	_, output := p.resolveControls(req, model)
	if output <= 0 {
		output = ollamaDefaultCompletionReserve
	}
	return modelcall.RequestBudget{
		MinimumOutput: modelcall.MinimumCompletionOutput(req),
		Window:        p.ContextLimit(ctx, model),
		Input:         p.projectPromptTokens(model, estimatePromptTokens(messages, ollamaCallableTools(req))),
		Output:        output, Headroom: ollamaContextHeadroom,
	}
}
