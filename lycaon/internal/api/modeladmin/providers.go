// Package modeladmin serves provider configuration, discovery, and model policy operations.
package modeladmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var providerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

type Handler struct {
	service   *llm.Service
	projects  project.Registry
	events    events.ReplayHub
	responses *httpio.Responder
}

func New(service *llm.Service, projects project.Registry, events events.ReplayHub, responses *httpio.Responder) *Handler {
	httpio.RequireDependencies("modeladmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "service", Present: service != nil},
		httpio.Required{Name: "service.Catalog", Present: service != nil && service.Catalog != nil},
		httpio.Required{Name: "service.Registry", Present: service != nil && service.Registry != nil},
		httpio.Required{Name: "projects", Present: projects != nil},
		httpio.Required{Name: "events", Present: events != nil},
	)
	return &Handler{service: service, projects: projects, events: events, responses: responses}
}

func (s *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	providers := s.service.Registry.ListCached(r.Context())
	if providers == nil {
		providers = []wire.ProviderMeta{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProviderListResponse{Providers: providers})
}

// providerFields are the writable fields create and update share; raw tells an
// omitted field from an explicit empty value.
type providerFields struct {
	kind                string
	label               string
	baseURL             string
	apiKeyEnv           string
	requiresAPIKey      *bool
	secretScreenTrusted *bool
	models              []wire.ProviderModelConfig
	raw                 map[string]json.RawMessage
}

func (f providerFields) reset(key string) bool {
	return strings.TrimSpace(string(f.raw[key])) == "null"
}

func providerPatchString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (f providerFields) sent(key string) bool {
	_, ok := f.raw[key]
	return ok
}

// CreateProvider adds a provider instance under a client-chosen id.
func (s *Handler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	var req wire.CreateProviderRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	id := strings.TrimSpace(req.ID)
	if !providerIDPattern.MatchString(id) {
		s.rejectProviderField(w, "id", "must be letters, digits, dot, dash, or underscore")
		return
	}
	if _, exists := s.service.Catalog.Get(id); exists {
		s.responses.FailDetails(w, wire.ApiErrorCodeDuplicateId, map[string]any{"field": "id"}, "a provider already uses this id")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = id
	}
	s.writeProvider(w, r, id, llm.CatalogEntry{}, false, providerFields{
		kind: kind, label: req.Label, baseURL: req.BaseURL, apiKeyEnv: req.APIKeyEnv,
		requiresAPIKey: req.RequiresAPIKey, secretScreenTrusted: req.SecretScreenTrusted,
		models: req.Models, raw: raw,
	})
}

// UpdateProvider merges the sent fields into an existing provider instance.
func (s *Handler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	existing, ok := s.service.Catalog.Get(id)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
		return
	}
	var raw map[string]json.RawMessage
	var req wire.UpdateProviderRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	s.writeProvider(w, r, id, existing, true, providerFields{
		kind: existing.Kind, label: providerPatchString(req.Label), baseURL: providerPatchString(req.BaseURL), apiKeyEnv: providerPatchString(req.APIKeyEnv),
		requiresAPIKey: req.RequiresAPIKey, secretScreenTrusted: req.SecretScreenTrusted,
		models: req.Models, raw: raw,
	})
}

func (s *Handler) rejectProviderField(w http.ResponseWriter, field, reason string) {
	s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": field, "reason": reason}, field+" "+reason)
}

// writeProvider validates the fields against the kind's catalog template,
// stores the instance, and answers it: 201 for a new instance, 200 otherwise.
func (s *Handler) writeProvider(w http.ResponseWriter, r *http.Request, id string, existing llm.CatalogEntry, hasExisting bool, f providerFields) {
	kind := f.kind
	ship, hasShip := s.service.Catalog.ShipEntryForKind(kind)
	endpointStyle := llm.EndpointStyleURL
	if hasExisting {
		endpointStyle = existing.EndpointStyle.Normalize()
	} else if hasShip {
		endpointStyle = ship.EndpointStyle.Normalize()
	}

	baseURL := strings.TrimSpace(f.baseURL)
	if f.reset("base_url") {
		baseURL = ship.BaseURL
	} else if !f.sent("base_url") {
		switch {
		case hasExisting:
			baseURL = existing.BaseURL
		case hasShip:
			baseURL = ship.BaseURL
		}
	}
	if err := validateProviderBaseURL(endpointStyle, baseURL); err != nil {
		s.rejectProviderField(w, "base_url", err.Error())
		return
	}
	if err := llm.ValidateProviderProtocolBase(kind, baseURL); err != nil {
		s.rejectProviderField(w, "base_url", "does not match this provider kind's protocol")
		return
	}
	if hasShip && !llm.PlatformsSupportedHere(ship.Platforms) {
		s.rejectProviderField(w, "kind", "is not supported on this host")
		return
	}
	label := strings.TrimSpace(f.label)
	if f.reset("label") {
		label = id
	} else if !f.sent("label") && hasExisting {
		label = existing.Label
	} else if f.sent("label") {
		// An empty label on a new instance displays the instance id.
		if hasExisting && label == "" {
			s.rejectProviderField(w, "label", "must not be empty")
			return
		}
		if label != "" {
			if err := validateProviderDisplayLabel(label); err != nil {
				s.rejectProviderField(w, "label", err.Error())
				return
			}
		}
	}
	apiKeyEnv := strings.TrimSpace(f.apiKeyEnv)
	if f.reset("api_key_env") {
		apiKeyEnv = ship.APIKeyEnv
	} else if !f.sent("api_key_env") {
		if hasExisting {
			apiKeyEnv = existing.APIKeyEnv
		} else if hasShip {
			// New instances inherit the template's display hint.
			apiKeyEnv = ship.APIKeyEnv
		}
	}
	var requiresAPIKey *bool
	switch {
	case f.sent("requires_api_key"):
		requiresAPIKey = f.requiresAPIKey
	case hasExisting:
		requiresAPIKey = existing.LocalRequiresAPIKey()
	}

	var models []modelinfo.Entry
	if f.sent("models") {
		models = make([]modelinfo.Entry, 0, len(f.models))
		for _, m := range f.models {
			modelID := strings.TrimSpace(m.ID)
			if modelID == "" {
				s.rejectProviderField(w, "models", "every model needs an id")
				return
			}
			var capabilities modelinfo.ModelCapabilities
			if m.Capabilities != nil {
				var capErr error
				capabilities, capErr = providerCapabilitiesFromWire(*m.Capabilities)
				if capErr != nil {
					s.rejectProviderField(w, "models", capErr.Error())
					return
				}
			}
			models = append(models, providerModelUpdate(m, capabilities, existing.Models))
		}
	} else if hasExisting {
		models = append([]modelinfo.Entry(nil), existing.Models...)
	}
	// Discovery supplies models when no override is stored.
	var httpRetryOverride *providerretry.ProviderHTTPRetry
	if hasExisting && existing.HTTPRetryOverride {
		cloned := existing.HTTPRetry.Clone()
		httpRetryOverride = &cloned
	}

	entry := llm.ProviderEntry{
		ID:                id,
		Kind:              kind,
		Label:             label,
		BaseURL:           baseURL,
		EndpointStyle:     endpointStyle,
		APIKeyEnv:         apiKeyEnv,
		RequiresAPIKey:    requiresAPIKey,
		Models:            models,
		HTTPRetryOverride: httpRetryOverride,
		RejectionReasons:  existing.LocalRejectionReasons(),
		ReasoningWire:     existing.LocalReasoningWire(),
	}
	trust, operation, err := s.providerSecretScreenTrust(entry, existing, f.sent("secret_screen_trusted"), f.secretScreenTrusted)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	entry.SecretScreenTrust, entry.SecretScreenTrustOperation = trust, operation
	if err := s.service.PutProvider(r.Context(), entry); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	status, action := http.StatusOK, "updated"
	if !hasExisting {
		status, action = http.StatusCreated, "created"
	}
	s.publishProviderEvent(r.Context(), action, id)
	httpio.WriteJSON(w, status, s.findProviderMeta(r.Context(), id))
}

// providerSecretScreenTrust binds an explicit decision to the destination this
// update resolves, with no installing operation. An omitted field keeps the
// stored decision and its operation only while it still names that destination.
func (s *Handler) providerSecretScreenTrust(entry llm.ProviderEntry, existing llm.CatalogEntry, sent bool, requested *bool) (trust, operation string, err error) {
	resolved, err := s.service.Catalog.ResolveEntry(entry)
	if err != nil {
		return "", "", err
	}
	destination := resolved.SecretDestinationID()
	if sent {
		if requested != nil && *requested {
			return destination, "", nil
		}
		return "", "", nil
	}
	if existing.SecretScreenTrust == destination {
		return destination, existing.SecretScreenTrustOperation, nil
	}
	return "", "", nil
}

func (s *Handler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	removed, err := s.service.DeleteProvider(r.Context(), id)
	if err != nil {
		var inUse *llm.ProviderInUseError
		if errors.As(err, &inUse) {
			s.responses.Fail(w, wire.ApiErrorCodeProviderInUse, "a model assignment still uses this provider")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if !removed {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
		return
	}
	s.publishProviderEvent(r.Context(), "deleted", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) ListProviderKinds(w http.ResponseWriter, r *http.Request) {
	templates := s.service.Catalog.KindTemplates()
	out := make([]wire.ProviderKindTemplate, 0, len(templates))
	for _, t := range templates {
		out = append(out, wire.ProviderKindTemplate{
			Kind:           t.Kind,
			Label:          t.Label,
			BaseURL:        t.BaseURL,
			EndpointStyle:  string(t.EndpointStyle.Normalize()),
			RequiresAPIKey: t.RequiresAPIKey,
			AmbientAuth:    t.AmbientAuth,
			Platforms:      append([]string(nil), t.Platforms...),
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProviderKindListResponse{Kinds: out})
}

func (s *Handler) findProviderMeta(ctx context.Context, id string) wire.ProviderMeta {
	for _, m := range s.service.Registry.List(ctx) {
		if m.ID == id {
			return m
		}
	}
	return wire.ProviderMeta{ID: id, Models: []wire.ProviderModelMeta{}}
}

func (s *Handler) publishProviderEvent(ctx context.Context, action, providerID string) {
	key := events.PublishKeyFor(ctx, project.ScopeLookup{Registry: s.projects}, "", "")
	_ = s.events.Publish(ctx, wire.EventTopicProviders, key, wire.ProviderEvent{
		ProviderID: providerID,
		Action:     action,
	})
}
