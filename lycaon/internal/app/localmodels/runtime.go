package localmodels

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/modelstore"
)

// Options names packaged resources and device-persistent model storage.
type Options struct {
	Binary       string
	MetalLibrary string
	Authority    string
	Seed         string
	StoreRoot    string
	PhysicalRAM  uint64
	Decision     bialy.Config
}

// Runtime retains the shared admission authority for both native processes.
type Runtime struct {
	Generation *nativegen.Runtime
	Decision   bialy.Config
	Store      *modelstore.Store
	Resources  *Resources
	Backend    string
}

// Build authenticates installed artifacts before constructing local inference.
func Build(ctx context.Context, options Options) (*Runtime, error) {
	if options.Binary == "" || options.StoreRoot == "" || options.Decision.Disabled {
		return nil, errors.New("local models: incomplete runtime configuration")
	}
	authority, err := readAuthority(options.Authority)
	if err != nil {
		return nil, err
	}
	inventory, err := nativegen.InspectDevices(ctx, options.Binary, options.MetalLibrary)
	if err != nil {
		return nil, err
	}
	requested, err := requestedDevice(options.Decision.Device, inventory.Backend)
	if err != nil {
		return nil, err
	}
	device, err := inference.SelectDevice(inventory, requested)
	if err != nil {
		return nil, err
	}
	plan, err := bialy.PlanMemory(options.Decision, inventory.Backend, 2)
	if err != nil {
		return nil, fmt.Errorf("local models: decision memory plan: %w", err)
	}
	resources, err := AllocateResources(options.PhysicalRAM, device, plan)
	if err != nil {
		return nil, err
	}
	store := modelstore.New(options.StoreRoot, authority)
	if store.Status(nativegen.DefaultModelID, inventory.Backend).State == modelstore.StateAbsent {
		if err := store.ImportBundle(ctx, options.Seed, modelstore.Slot{Model: nativegen.DefaultModelID, Device: inventory.Backend}); err != nil {
			resources.Close()
			return nil, err
		}
	}
	decision := options.Decision
	decision.Resources = resources.Decision
	return &Runtime{
		Decision: decision, Store: store, Resources: resources, Backend: inventory.Backend,
		Generation: nativegen.NewRuntime(nativegen.RuntimeConfig{
			BinaryPath: options.Binary, MetalLibrary: options.MetalLibrary,
			Backend: inventory.Backend, PhysicalDevice: device.PhysicalDevice,
			Devices: resources.Devices, HostMemory: resources.GenerationHost,
			DeviceMemory: resources.GenerationDevice, UnifiedMemory: device.UnifiedMemory,
			ModelStore: store, SeedDirectory: options.Seed, SharedWorkspaceBytes: resources.SharedWorkspaceBytes,
		}),
	}, nil
}

func readAuthority(path string) (*modelstore.Authority, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, modelstore.MaxAuthorityBytes+1))
	if err != nil {
		return nil, err
	}
	return modelstore.DecodeAuthority(raw)
}

func requestedDevice(value, backend string) (*inference.PhysicalDevice, error) {
	switch value {
	case "", "auto":
		return nil, nil
	case "mlx", "vulkan":
		if value == backend {
			return nil, nil
		}
	default:
		if address, ok := strings.CutPrefix(value, "vulkan-pci:"); ok && backend == "vulkan" {
			return &inference.PhysicalDevice{Kind: "pci", Address: address}, nil
		}
	}
	return nil, errors.New("local models: selected decision device cannot share this generation adapter")
}
