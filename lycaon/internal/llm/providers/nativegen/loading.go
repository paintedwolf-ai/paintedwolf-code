package nativegen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/modelstore"
)

const startupHostBytes = 768 << 20

func (p *Runtime) ensureLoaded(ctx context.Context, model string) error {
	if err := p.validate(); err != nil {
		return err
	}
	if p.loaded != nil {
		select {
		case <-p.loaded.process.done:
			p.unload()
		default:
			if p.loaded.model == model {
				active, err := p.config.ModelStore.Active()
				if err != nil {
					p.unload()
					return err
				}
				manifest, ok := active[model+"/"+p.config.Backend]
				if ok && manifest.Revision == p.loaded.process.lease.Manifest.Revision {
					return nil
				}
			}
			p.unload()
		}
	}
	runtime := &p.config
	device, err := runtime.Devices.Bind(runtime.PhysicalDevice)
	if err != nil {
		return err
	}
	p.generation++
	lease, err := runtime.ModelStore.AcquireLease(model, runtime.Backend, "local-generation", p.generation)
	if err != nil {
		return err
	}
	if err := lease.Audit(ctx); err != nil {
		lease.Release()
		return err
	}
	identity, err := modelstore.SigningBytes(lease.Manifest)
	if err != nil {
		lease.Release()
		return err
	}
	digest := sha256.Sum256(identity)
	artifact := hex.EncodeToString(digest[:])
	reservation, err := runtime.HostMemory.Reserve(startupHostBytes)
	if err != nil {
		lease.Release()
		return err
	}
	child, err := startProcess(ctx, processSpec{binary: runtime.BinaryPath, directory: lease.CheckpointDir(), artifact: artifact, metallib: runtime.MetalLibrary, pci: runtime.PhysicalDevice.Address}, lease, device, "local-generation", p.generation)
	if err != nil {
		reservation.Release()
		return err
	}
	if err := child.retain(reservation); err != nil {
		child.close()
		return err
	}
	loaded, err := p.initialize(ctx, child, artifact)
	if err != nil {
		child.close()
		return err
	}
	p.loaded = &loadedProcess{process: child, model: model, sampling: loaded.Sampling}
	return nil
}

func (p *Runtime) initialize(ctx context.Context, child *rpcProcess, artifact string) (loadedResult, error) {
	runtime := &p.config
	var hello helloResult
	if err := child.call(ctx, command{Method: "hello"}, "hello", inference.Foreground, false, &hello); err != nil {
		return loadedResult{}, err
	}
	if hello.Protocol != 1 || hello.ArtifactID != artifact || hello.Backend != runtime.Backend {
		return loadedResult{}, errors.New("native generation: runtime identity mismatch")
	}
	actual, err := runtime.Devices.Bind(hello.PhysicalDevice)
	if err != nil || actual != child.device {
		return loadedResult{}, errors.New("native generation: physical device mismatch")
	}
	var resources resourceResult
	limits := command{Method: "inspect", ContextTokens: p.config.ContextLimit, PrefillChunk: p.config.PrefillChunk}
	if err := child.call(ctx, limits, "resources", inference.Foreground, false, &resources); err != nil {
		return loadedResult{}, err
	}
	plan := resources.Plan
	if plan.HostReservationBytes < startupHostBytes {
		return loadedResult{}, errors.New("native generation: runtime omitted its startup reservation")
	}
	if err := reserveLoaded(child, runtime, plan); err != nil {
		return loadedResult{}, err
	}
	limits.Method = "load"
	limits.DeviceBudgetBytes, limits.HostBudgetBytes = plan.DeviceReservationBytes, plan.HostReservationBytes
	var loaded loadedResult
	if err := child.call(ctx, limits, "loaded", inference.Foreground, true, &loaded); err != nil {
		return loadedResult{}, err
	}
	if loaded.ContextTokens != p.config.ContextLimit || loaded.PrefillChunk != p.config.PrefillChunk || math.IsNaN(float64(loaded.Sampling.Temperature)) || math.IsInf(float64(loaded.Sampling.Temperature), 0) || loaded.Sampling.Temperature < 0 || !(loaded.Sampling.TopP > 0 && loaded.Sampling.TopP <= 1) || loaded.Sampling.TopK > 262144 {
		return loadedResult{}, errors.New("native generation: runtime execution settings mismatch")
	}
	return loaded, nil
}

func reserveLoaded(child *rpcProcess, runtime *RuntimeConfig, plan resourcePlan) error {
	reservations, err := inference.ReserveMemory(runtime.HostMemory, runtime.DeviceMemory,
		plan.HostReservationBytes-startupHostBytes, plan.DeviceReservationBytes, runtime.UnifiedMemory)
	if err != nil {
		return err
	}
	return child.retain(reservations...)
}
