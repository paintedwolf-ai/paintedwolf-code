package nativegen

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

const (
	// DefaultModelID identifies the bundled Gemma 4 E4B model.
	DefaultModelID = "gemma4-e4b-paintedwolf"
	// DefaultContextWindowTokens caps the artifact context window.
	DefaultContextWindowTokens = 65536
	// DefaultOutputReserveTokens applies when MaxTokens is unset.
	DefaultOutputReserveTokens = 2048
	// MaxOutputReserveTokens bounds requested completion tokens on the local model.
	MaxOutputReserveTokens = 4096
)

// ErrContextCapacityExceeded includes the requested output reserve.
var ErrContextCapacityExceeded = errors.New("native generation: request exceeds admitted context capacity")

// DefaultModelEntry returns the catalog descriptor for the bundled Gemma 4 E4B model.
func DefaultModelEntry() modelinfo.Entry {
	return modelinfo.Entry{
		ID:            DefaultModelID,
		ContextLength: DefaultContextWindowTokens,
		MaxTokens:     MaxOutputReserveTokens,
		Callable:      modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
		Capabilities: modelinfo.ModelCapabilities{
			Chat:             modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Streaming:        modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Tools:            modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			StructuredOutput: modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			PromptCaching:    modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Vision:           modelinfo.Evidence(modelinfo.CapabilityUnsupported, "nativegen"),
		},
	}
}

func (p *Provider) ModelEntry(model string) (modelinfo.Entry, bool) {
	entry, ok := p.Catalog.ModelEntry(model)
	if ok && p.runtime != nil {
		window := p.runtime.ContextWindow()
		if entry.ContextLength <= 0 || entry.ContextLength > window {
			entry.ContextLength = window
		}
	}
	return entry, ok
}

func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	budget := p.Catalog.RequestBudget(ctx, req)
	entry, _ := p.ModelEntry(p.ResolveModel(req))
	budget.Window = entry.ContextLength
	budget.Output = outputLimit(req)
	budget.MinimumOutput = 0
	return budget
}

func outputLimit(req modelcall.CompletionRequest) int {
	limit := modelcall.CompletionTokenLimit(req, MaxOutputReserveTokens, DefaultOutputReserveTokens, modelcall.ThinkOff, false)
	if req.MaxTokens <= 0 {
		limit = min(limit, DefaultOutputReserveTokens)
	}
	return limit
}

// DiscoverModels publishes only a successfully admitted model.
func (p *Runtime) DiscoverModels(ctx context.Context) ([]modelinfo.Entry, error) {
	if p == nil {
		return nil, errors.New("native generation: host runtime is unavailable")
	}
	ctx, release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := p.ensureLoaded(ctx, DefaultModelID); err != nil {
		return nil, err
	}
	entry := DefaultModelEntry()
	entry.ContextLength = p.ContextWindow()
	entry.ContextLengthSource = "native-runtime"
	return []modelinfo.Entry{entry}, nil
}
