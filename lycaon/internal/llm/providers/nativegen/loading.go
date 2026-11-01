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

const startupHostBytes = 512 << 20

func (p *Runtime) ensureLoaded(ctx context.Context, model string) (err error) {
	defer func() { p.settleReadiness(err) }()
	return p.loadModel(ctx, model)
}

func (p *Runtime) loadModel(ctx context.Context, model string) error {
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
	p.publishReadiness("loading", "none")
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
	if (runtime.SharedWorkspaceBytes > 0 && !hello.QuiescentMemory) || hello.Protocol != 1 || hello.ArtifactID != artifact || hello.Backend != runtime.Backend {
		return loadedResult{}, errors.New("native generation: runtime identity mismatch")
	}
	actual, err := runtime.Devices.Bind(hello.PhysicalDevice)
	if err != nil || actual != child.device {
		return loadedResult{}, errors.New("native generation: physical device mismatch")
	}
	limits, err := p.admitContext(ctx, child)
	if err != nil {
		return loadedResult{}, err
	}
	var loaded loadedResult
	if err := child.call(ctx, limits, "loaded", inference.Foreground, true, &loaded); err != nil {
		return loadedResult{}, err
	}
	if loaded.ContextTokens != limits.ContextTokens || loaded.PrefillChunk != p.config.PrefillChunk || math.IsNaN(float64(loaded.Sampling.Temperature)) || math.IsInf(float64(loaded.Sampling.Temperature), 0) || loaded.Sampling.Temperature < 0 || !(loaded.Sampling.TopP > 0 && loaded.Sampling.TopP <= 1) || loaded.Sampling.TopK > 262144 {
		return loadedResult{}, errors.New("native generation: runtime execution settings mismatch")
	}
	p.window.Store(int64(loaded.ContextTokens))
	return loaded, nil
}

func reserveLoaded(child *rpcProcess, runtime *RuntimeConfig, plan resourcePlan) error {
	// The decision partition retains the physical workspace across both process lifetimes.
	deviceBytes := plan.DeviceReservationBytes - min(plan.WorkspaceBytes, runtime.SharedWorkspaceBytes)
	reservations, err := inference.ReserveMemory(runtime.HostMemory, runtime.DeviceMemory,
		plan.HostReservationBytes-startupHostBytes, deviceBytes, runtime.UnifiedMemory)
	if err != nil {
		return err
	}
	return child.retain(reservations...)
}
