package localmodels

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/modelstore"
)

// Options names packaged resources and device-persistent model storage.
type Options struct {
	Store        *modelstore.Store
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

// Build binds both adapters to one device and resource partition.
func Build(ctx context.Context, options Options) (*Runtime, error) {
	if options.Binary == "" || options.StoreRoot == "" || options.Decision.Disabled {
		return nil, errors.New("local models: incomplete runtime configuration")
	}
	store := options.Store
	if store == nil {
		authority, err := readAuthority(options.Authority)
		if err != nil {
			return nil, err
		}
		store = modelstore.New(options.StoreRoot, authority)
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
	decision := options.Decision
	decision.Resources = resources.Decision
	physicalBytes := device.TotalBytes
	if device.UnifiedMemory {
		physicalBytes = options.PhysicalRAM
	}
	r := &Runtime{
		Decision: decision, Store: store, Resources: resources, Backend: inventory.Backend,
		Generation: nativegen.NewRuntime(nativegen.RuntimeConfig{
			DecisionDeviceBytes: plan.DeviceBytes, PhysicalBytes: physicalBytes, BinaryPath: options.Binary, MetalLibrary: options.MetalLibrary,
			Backend: inventory.Backend, PhysicalDevice: device.PhysicalDevice,
			Devices: resources.Devices, HostMemory: resources.GenerationHost,
			DeviceMemory: resources.GenerationDevice, UnifiedMemory: device.UnifiedMemory,
			ModelStore: store, SeedDirectory: options.Seed, SharedWorkspaceBytes: resources.SharedWorkspaceBytes,
		}),
	}
	if _, err := store.Collect(); err != nil {
		r.Generation.ReportSelectionError(err)
	}
	return r, nil
}

func readAuthority(path string) (*modelstore.Authority, error) {
	raw, err := readPackagedMetadata(path, modelstore.MaxAuthorityBytes)
	if errors.Is(err, os.ErrNotExist) {
		return modelstore.NewAuthority(nil)
	}
	if err != nil {
		return nil, err
	}
	return modelstore.DecodeAuthority(raw)
}

func readPackagedMetadata(path string, limit int64) (data []byte, resultErr error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("local models: invalid packaged metadata at %s", path)
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("local models: packaged metadata exceeds %d bytes", limit)
	}
	return data, err
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
