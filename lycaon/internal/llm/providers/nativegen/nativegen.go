// Package nativegen drives the admitted local generation service.
package nativegen

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/modelstore"
)

// RuntimeConfig binds the packaged adapter to host-owned resource admission.
type RuntimeConfig struct {
	ContextLimit         int
	PrefillChunk         int
	BinaryPath           string
	MetalLibrary         string
	Backend              string
	PhysicalDevice       inference.PhysicalDevice
	Devices              *inference.Devices
	HostMemory           *inference.MemoryBudget
	DeviceMemory         *inference.MemoryBudget
	UnifiedMemory        bool
	SharedWorkspaceBytes uint64
	ModelStore           *modelstore.Store
}

type Options struct {
	Runtime *Runtime
	Models  []modelinfo.Entry
}

type Provider struct {
	modelcall.Catalog
	id      string
	runtime *Runtime
}

func New(id string, opts Options) *Provider {
	if len(opts.Models) == 0 {
		opts.Models = []modelinfo.Entry{DefaultModelEntry()}
	}
	return &Provider{id: id, runtime: opts.Runtime, Catalog: modelcall.NewCatalog("nativegen", opts.Models)}
}

type loadedProcess struct {
	process  *rpcProcess
	model    string
	sampling samplingDefaults
	request  uint64
}

// Runtime retains one model process across provider snapshots and roles.
type Runtime struct {
	config     RuntimeConfig
	lifetime   context.Context
	cancel     context.CancelFunc
	gate       chan struct{}
	loaded     *loadedProcess
	prepared   *preparedPrompt
	generation uint64
	window     atomic.Int64
}

func NewRuntime(config RuntimeConfig) *Runtime {
	if config.ContextLimit == 0 {
		config.ContextLimit = DefaultContextWindowTokens
	}
	if config.PrefillChunk == 0 {
		config.PrefillChunk = 128
	}
	lifetime, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{config: config, lifetime: lifetime, cancel: cancel, gate: make(chan struct{}, 1)}
	runtime.window.Store(int64(config.ContextLimit))
	runtime.gate <- struct{}{}
	return runtime
}

// ContextWindow reports the admitted window once the runtime has warmed.
func (p *Runtime) ContextWindow() int { return int(p.window.Load()) }

// Warm establishes the model process and its admitted context before role selection.
func (p *Runtime) Warm(ctx context.Context, model string) error {
	ctx, release, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return p.ensureLoaded(ctx, model)
}

func (p *Provider) ID() string { return p.id }

func (p *Provider) Profile() providerprofile.Profile {
	profile := providerprofile.LocalInference(providerprofile.Profile{
		ToolCalls:     providerprofile.ToolCallsNative,
		Streaming:     providerprofile.StreamNative,
		Discovery:     providerprofile.DiscoveryNativeGeneration,
		ReasoningWire: providerprofile.ReasoningWireNone,
	})
	return profile
}

func (p *Runtime) acquire(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.lifetime, cancel)
	release := func() { stop(); cancel(); p.gate <- struct{}{} }
	select {
	case <-ctx.Done():
		stop()
		cancel()
		return nil, nil, ctx.Err()
	case <-p.gate:
		if p.lifetime.Err() != nil || ctx.Err() != nil {
			release()
			return nil, nil, context.Canceled
		}
		return ctx, release, nil
	}
}

func (p *Runtime) Close() error {
	p.cancel()
	<-p.gate
	defer func() { p.gate <- struct{}{} }()
	p.unload()
	return nil
}

func (p *Runtime) unload() {
	p.prepared = nil
	if p.loaded != nil {
		p.loaded.process.close()
		p.loaded = nil
	}
}

func (p *Runtime) validate() error {
	r := &p.config
	if r.BinaryPath == "" || r.ModelStore == nil || r.Devices == nil || r.HostMemory == nil || r.DeviceMemory == nil || r.HostMemory == r.DeviceMemory {
		return errors.New("native generation: local runtime is not configured")
	}
	if p.config.ContextLimit < 2 || p.config.ContextLimit > 65536 || p.config.PrefillChunk < 1 || p.config.PrefillChunk > 256 {
		return errors.New("native generation: invalid execution limits")
	}
	if r.SharedWorkspaceBytes > 0 && r.Backend != "mlx" {
		return errors.New("native generation: adapter cannot share transient workspace")
	}
	switch r.Backend {
	case "mlx":
		if r.PhysicalDevice.Kind != "apple_silicon" || r.MetalLibrary == "" || !r.UnifiedMemory {
			return errors.New("native generation: invalid unified-memory adapter")
		}
	case "vulkan":
		if r.PhysicalDevice.Kind != "pci" {
			return errors.New("native generation: invalid discrete-memory adapter")
		}
	default:
		return errors.New("native generation: unsupported backend")
	}
	return nil
}
