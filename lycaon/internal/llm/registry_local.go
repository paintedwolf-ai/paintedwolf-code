package llm

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/pkg/api"
)

func (r *registryCore) LocalGenerationStatus() api.LocalGenerationStatus {
	status := r.localRuntime.Readiness()
	return api.LocalGenerationStatus{
		State: string(status.State), RecoveryAction: string(status.Action), Detail: status.Detail,
		Revision: status.Revision, ContextTokens: status.ContextTokens,
	}
}

// projectLocalRuntime overlays live admission on cached catalog metadata.
func (r *registryCatalog) projectLocalRuntime(in []api.ProviderMeta) []api.ProviderMeta {
	out := cloneProviderMetaList(in)
	for i := range out {
		if out[i].Kind != "nativegen" {
			continue
		}
		status := r.LocalGenerationStatus()
		out[i].LocalRuntime = &status
		out[i].Configured = status.State == "ready"
		if !out[i].Configured {
			out[i].ReadyToAssign = false
		}
	}
	return out
}

func (r *Registry) RepairLocalGeneration(ctx context.Context, id string) error {
	entry, exists := r.catalog.Get(id)
	if !exists || entry.Kind != "nativegen" {
		return errors.New("provider does not use bundled local generation")
	}
	if r.localRuntime == nil {
		return errors.New("bundled local generation runtime is unavailable")
	}
	err := r.localRuntime.Repair(ctx)
	r.RefreshDiscovery(id)
	if err != nil {
		return err
	}
	return r.RefreshModels(ctx, id)
}
