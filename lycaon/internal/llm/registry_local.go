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
		OfflineInstaller: r.offlineInstaller, State: string(status.State), RecoveryAction: string(status.Action), Detail: status.Detail,
		Revision: status.Revision, ContextTokens: status.ContextTokens,
	}
	r.localRuntime.SelectionStatus(&out)
	if r.localRuntime == nil && r.localStore != nil {
		selection, err := r.localStore.Selection()
		if err != nil {
			out.Detail = err.Error()
		} else {
			out.SelectionState = selection.State
			out.SelectedModel = selection.Model
			if selection.State == "opted_out" && out.State != "unavailable" {
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

func (s *Service) SelectLocalModel(req api.LocalModelSelectionRequest, settled func()) error {
	return s.Registry.localRuntime.StartSelection(req, s.initializeLocalRoles, func() { s.Registry.RefreshDiscovery(IncludedProviderID); s.ResetPlanes(); settled() })
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

// Save the initial roles together so later edits can change them independently.
func (s *Service) initializeLocalRoles(model string) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	overlay, err := s.Policy.Overlay(SettingsScopeGlobal, "")
	if err != nil {
		return err
	}
	effective, err := s.Policy.Get(SettingsScopeGlobal, "")
	if err != nil {
		return err
	}
	initial := !modelRefSet(overlay.Coordinator)
	coordinator := selectedLocalRef(effective.Coordinator, model)
	if !modelRefSet(coordinator) {
		coordinator = ModelRef{ProviderID: IncludedProviderID, Model: model}
	}
	lite := selectedLocalRef(overlay.Lite, model)
	if initial && !modelRefSet(effective.Lite) {
		lite = coordinator
	}
	changed := overlay.Coordinator != coordinator || overlay.Lite != lite
	overlay.Coordinator, overlay.Lite = coordinator, lite
	for i, ref := range overlay.AgentPool.Models {
		updated := selectedLocalRef(ref, model)
		if updated != ref {
			overlay.AgentPool.Models[i] = updated
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.Policy.PutGlobal(overlay)
}
