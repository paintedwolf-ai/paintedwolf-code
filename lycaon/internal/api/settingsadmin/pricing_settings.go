package settingsadmin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetPricingSettings(w http.ResponseWriter, r *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, s.Pricing.Response())
}

func (s *Handler) HandleUpdatePricingSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	var req wire.SettingsPricing
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	eff := s.Pricing.Store.Effective()
	costTracking := eff.CostTrackingEnabled
	if _, ok := raw["cost_tracking_enabled"]; ok {
		if raw["cost_tracking_enabled"] == nil {
			costTracking = false
		} else {
			costTracking = req.CostTrackingEnabled
		}
	}
	sources := make([]settings.PricingSourcePref, 0, len(eff.Sources))
	if _, ok := raw["sources"]; ok {
		if raw["sources"] == nil {
			sources = nil
		} else {
			for _, src := range req.Sources {
				sources = append(sources, settings.PricingSourcePref{ID: src.ID, Enabled: src.Enabled})
			}
		}
	} else {
		for _, src := range eff.Sources {
			sources = append(sources, settings.PricingSourcePref{ID: src.ID, Enabled: src.Enabled})
		}
	}
	overlay := settings.PricingUserOverlay{
		CostTrackingEnabled: pricingBoolPtr(costTracking),
		Sources:             sources,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.Pricing.ApplySettings(ctx, overlay); err != nil {
		if errors.Is(err, settings.ErrPricingNoSource) {
			s.responses.Fail(w, wire.ApiErrorCodePricingNoSource, "enable at least one pricing source before turning cost tracking on")
			return
		}
		if errors.Is(err, settings.ErrPricingMultipleSources) {
			s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "sources", "reason": "name one pricing source"}, "name one pricing source")
			return
		}
		if errors.Is(err, settings.ErrPricingUnknownSource) {
			s.responses.Fail(w, wire.ApiErrorCodePricingSourceNotFound, "the pricing source is not configured")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaPricing, "global", "", "updated")
	httpio.WriteJSON(w, http.StatusOK, s.Pricing.Response())
}

func (s *Handler) HandleRefreshPricingSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "source_id")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	meta, err := s.Pricing.RefreshSource(ctx, id)
	if err != nil {
		code := settings.RefreshErrorCode(err)
		if code == wire.ApiErrorCodeInternalError {
			s.responses.InternalError(w, r, err)
			return
		}
		s.responses.Fail(w, code, "pricing source refresh failed")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, meta)
}

func pricingBoolPtr(v bool) *bool { return &v }
