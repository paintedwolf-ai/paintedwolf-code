package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/harmony"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
)

type dispatchObserverKey struct{}
type dispatchObserver struct {
	once   sync.Once
	notify func()
}

// WithDispatchObserver observes the first dispatch after preparation and screening.
func WithDispatchObserver(ctx context.Context, notify func()) context.Context {
	return context.WithValue(ctx, dispatchObserverKey{}, &dispatchObserver{notify: notify})
}

func notifyDispatch(ctx context.Context) {
	observer, _ := ctx.Value(dispatchObserverKey{}).(*dispatchObserver)
	if observer != nil && observer.notify != nil {
		observer.once.Do(observer.notify)
	}
}

type dispatchClient struct{ modelcall.LLMClient }

// WithClientDispatch supplies the dispatch boundary for directly injected clients.
// Registry clients publish at their screened provider boundary instead.
func WithClientDispatch(client modelcall.LLMClient) modelcall.LLMClient {
	if client == nil {
		return nil
	}
	return &dispatchClient{LLMClient: client}
}

func (c *dispatchClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	notifyDispatch(ctx)
	return c.LLMClient.Complete(ctx, req)
}

func (c *dispatchClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	notifyDispatch(ctx)
	return c.LLMClient.Stream(ctx, req)
}

// ModelResidencyReporter is a local driver that can say whether its runner
// still holds a model, and with it the KV state of the last prefix served.
type ModelResidencyReporter interface {
	ModelResident(ctx context.Context, model string) (resident, known bool)
}

// PromptCachePolicy returns the prompt-cache policy one route runs under:
// the provider kind's policy refined for the model. ok is false for an
// unknown provider.
func (r *registryRouting) PromptCachePolicy(providerID, model string) (providerprofile.PromptCachePolicy, bool) {
	if r == nil {
		return providerprofile.PromptCachePolicy{}, false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return providerprofile.PromptCachePolicy{}, false
	}
	entry, ok := snapshot.entries[strings.TrimSpace(providerID)]
	if !ok {
		return providerprofile.PromptCachePolicy{}, false
	}
	return entry.PromptCache.ForModel(model), true
}

// ModelResident asks a route's local runner whether it still holds the
// model. known is false when the driver has no probe or the runner cannot say.
func (r *registryRouting) ModelResident(ctx context.Context, providerID, model string) (resident, known bool) {
	if r == nil {
		return false, false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return false, false
	}
	reporter, ok := snapshot.providers[strings.TrimSpace(providerID)].(ModelResidencyReporter)
	if !ok {
		return false, false
	}
	return reporter.ModelResident(ctx, model)
}

type ToolReasoningPolicy struct {
	Intent    string `json:"intent"`
	Style     string `json:"style"`
	AlwaysOn  bool   `json:"always_on"`
	DefaultOn bool   `json:"default_on"`
}

func toolDriverIdentity(profile providerprofile.Profile) (string, error) {
	data, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func toolReasoningPolicy(profile providerprofile.Profile, model modelinfo.Entry) ToolReasoningPolicy {
	thinking := modelcall.ResolveModelThinking(profile, model, model.ID)
	intent := "medium"
	switch modelcall.ResolveThinkLevel(modelcall.CompletionRequest{}, model.ReasoningEffort, false) {
	case modelcall.ThinkOff:
		intent = "off"
	case modelcall.ThinkLow:
		intent = "low"
	case modelcall.ThinkHigh:
		intent = "high"
	case modelcall.ThinkUnset, modelcall.ThinkMedium:
	}
	return ToolReasoningPolicy{Intent: intent, Style: string(thinking.Style), AlwaysOn: thinking.AlwaysOn, DefaultOn: thinking.DefaultOn}
}

func toolRequestIdentity(profile providerprofile.Profile, model modelinfo.Entry) (string, error) {
	data, err := json.Marshal(struct {
		Context     int
		Output      int
		Temperature *float64
		Effort      string
		Style       string
		Thinking    modelcall.ModelThinking
	}{model.ContextLength, model.MaxTokens, model.Temperature, model.ReasoningEffort, model.ThinkStyle, modelcall.ResolveModelThinking(profile, model, model.ID)})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

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
