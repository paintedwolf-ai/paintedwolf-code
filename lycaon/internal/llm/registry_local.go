package llm

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/pkg/api"
)

func (r *registryCore) LocalGenerationStatus() api.LocalGenerationStatus {
	status := r.localRuntime.Readiness()
	if r.localRuntime == nil && r.localStartup.State != "" {
		status = r.localStartup
	}
	out := api.LocalGenerationStatus{
		State: string(status.State), RecoveryAction: string(status.Action), Detail: status.Detail,
		Revision: status.Revision, ContextTokens: status.ContextTokens,
	}
	r.localRuntime.SelectionStatus(&out)
	if r.localRuntime == nil && r.localStore != nil {
		selection, err := r.localStore.Selection()
		if err != nil {
			out.Detail = err.Error()
		} else {
			out.SelectedModel = selection.Model
			if selection.State == "opted_out" {
				out.State = "opted_out"
				out.RecoveryAction = "none"
			}
		}
	}
	return out
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

func (r *Registry) SelectLocalModel(req api.LocalModelSelectionRequest, settled func()) error {
	return r.localRuntime.StartSelection(req, func() { r.RefreshDiscovery(IncludedProviderID); settled() })
}

func (r *Registry) RemoveLocalModel(ctx context.Context) error {
	if r.localRuntime == nil && r.localStore != nil {
		return r.localStore.PurgeAll()
	}
	err := r.localRuntime.RemoveModel(ctx)
	r.RefreshDiscovery(IncludedProviderID)
	return err
}

func (r *Registry) CancelLocalModelTransfer(ctx context.Context) error {
	return r.localRuntime.CancelTransfer(ctx)
}
