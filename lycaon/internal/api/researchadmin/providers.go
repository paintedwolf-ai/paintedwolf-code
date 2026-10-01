package researchadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webresearch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) GetProviders(w http.ResponseWriter, r *http.Request) {
	status := webresearch.BuildProvidersStatus(s.Runtime.Catalog, s.Runtime.Registry, s.Runtime.Config, s.Runtime.Creds, s.directSearchReady)
	httpio.WriteJSON(w, http.StatusOK, status)
}

func (s *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings := webresearch.BuildWebResearchSettings(s.Runtime.Catalog, s.Runtime.Config, s.Runtime.Creds)
	httpio.WriteJSON(w, http.StatusOK, settings)
}

func (s *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req wire.UpdateWebResearchSettingsRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	var enabled []string
	if req.EnabledProviders != nil {
		enabled = webresearch.FilterKnownProviderIDs(s.Runtime.Catalog, req.EnabledProviders)
		if enabled == nil {
			enabled = []string{}
		}
	}
	if req.Warming == nil && req.GuessDomains == nil && req.SearchEnabled == nil && enabled == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "no settings fields provided")
		return
	}
	if err := s.Runtime.Config.ApplyPrefs(req.Warming, req.GuessDomains, req.SearchEnabled, enabled); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.publishWebResearchEvent(r.Context(), "settings_updated", "")
	settings := webresearch.BuildWebResearchSettings(s.Runtime.Catalog, s.Runtime.Config, s.Runtime.Creds)
	httpio.WriteJSON(w, http.StatusOK, settings)
}

func (s *Handler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "provider_id"))
	if !webresearch.ValidProviderID(s.Runtime.Catalog, id) {
		s.responses.Fail(w, wire.ApiErrorCodeResearchProviderNotFound, "unknown web research provider")
		return
	}
	var req wire.UpdateWebResearchProviderRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Config == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "config is required")
		return
	}
	entry, _ := s.Runtime.Catalog.Entry(id)
	if endpoint, ok := req.Config["endpoint"]; ok && strings.TrimSpace(endpoint) != "" {
		if err := webresearch.ValidateProviderConfigEndpoint(r.Context(), endpoint, entry.AllowPrivateEndpoint); err != nil {
			s.responses.Logger.DebugContext(r.Context(), "web research endpoint rejected", "provider_id", id, "err", err)
			s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "config.endpoint"},
				"the endpoint is not an allowed web research address")
			return
		}
	}
	if err := s.Runtime.Config.SetProviderConfig(id, req.Config); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.Runtime.InvalidateProvider(id)
	s.publishWebResearchEvent(r.Context(), "config_updated", id)
	meta, ok := webresearch.BuildProviderMeta(s.Runtime.Catalog, s.Runtime.Registry, s.Runtime.Config, s.Runtime.Creds, id)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeResearchProviderNotFound, "unknown web research provider")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, meta)
}

func (s *Handler) directSearchReady() bool {
	if !s.Runtime.Config.SearchEnabled() {
		return false
	}
	settings := webresearch.DefaultSettings(s.Runtime.Creds, s.Runtime.Config, s.Runtime.Catalog)
	for _, id := range settings.EnabledProviders {
		if id == "direct" {
			return true
		}
	}
	return false
}

func (s *Handler) publishWebResearchEvent(ctx context.Context, action, id string) {
	payload := action
	if id != "" {
		payload = action + ":" + id
	}
	_ = s.Events.Publish(ctx, wire.EventTopicSettings, events.PublishKey{Facet: string(wire.SettingsAreaWebResearch)}, wire.SettingsEvent{
		Area:   wire.SettingsAreaWebResearch,
		Action: payload,
	})
}

// providerCredentialSlot resolves the credential slot the addressed provider
// reads. A provider that takes no key has no credential to address.
func (s *Handler) providerCredentialSlot(w http.ResponseWriter, r *http.Request) (id, slot string, ok bool) {
	id = strings.TrimSpace(chi.URLParam(r, "provider_id"))
	entry, found := s.Runtime.Catalog.Entry(id)
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeResearchProviderNotFound, "unknown web research provider")
		return "", "", false
	}
	slot = entry.CredentialSlot
	if slot == "" {
		slot = entry.OptionalCredentialSlot
	}
	if slot == "" || !s.Runtime.Creds.ValidID(slot) {
		s.responses.Fail(w, wire.ApiErrorCodeResearchCredentialNotFound, "this web research provider takes no credential")
		return "", "", false
	}
	return id, slot, true
}

func (s *Handler) SetCredential(w http.ResponseWriter, r *http.Request) {
	id, slot, ok := s.providerCredentialSlot(w, r)
	if !ok {
		return
	}
	var req wire.SetProviderCredentialRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "api_key is required")
		return
	}
	if err := s.Runtime.Creds.Set(slot, apiKey); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.Runtime.InvalidateCredential(slot)
	s.publishWebResearchEvent(r.Context(), "credential_updated", id)
	meta, ok := webresearch.BuildProviderMeta(s.Runtime.Catalog, s.Runtime.Registry, s.Runtime.Config, s.Runtime.Creds, id)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeResearchProviderNotFound, "unknown web research provider")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, meta)
}

func (s *Handler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	id, slot, ok := s.providerCredentialSlot(w, r)
	if !ok {
		return
	}
	if err := s.Runtime.Creds.Delete(slot); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.Runtime.InvalidateCredential(slot)
	s.publishWebResearchEvent(r.Context(), "credential_deleted", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) TestProvider(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "provider_id"))
	start := time.Now()
	out, err := s.Runtime.TestProvider(r.Context(), id, s.Discoverer(r.Context(), tools.ToolContext{}))
	latency := time.Since(start).Milliseconds()
	if err != nil {
		var unknown webresearch.ErrUnknownProvider
		if errors.As(err, &unknown) {
			s.responses.Fail(w, wire.ApiErrorCodeResearchProviderNotFound, "unknown web research provider")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if !out.OK {
		s.responses.Logger.WarnContext(r.Context(), "web research provider probe failed", "provider_id", id, "diagnostic", out.Error)
		httpio.WriteJSON(w, http.StatusOK, wire.ProviderProbeResult{
			OK:        false,
			Message:   "The provider could not complete a search. Check its configuration and credentials.",
			LatencyMs: latency,
		})
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProviderProbeResult{OK: true, LatencyMs: latency})
}
