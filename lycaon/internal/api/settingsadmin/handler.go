package settingsadmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Deps are the settings routes' dependencies, fixed at construction.
type Deps struct {
	Pricing  *settings.PricingHost
	Power    *hostpower.Controller
	Events   events.ReplayHub
	Projects project.Registry
	Sessions *session.Host
	Service  *settings.Service
	Sources  *sourceapi.Handler
}

type Handler struct {
	Deps
	responses *httpio.Responder
}

func New(responses *httpio.Responder, deps Deps) Handler {
	httpio.RequireDependencies("settingsadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "Events", Present: deps.Events != nil},
		httpio.Required{Name: "Power", Present: deps.Power != nil},
		httpio.Required{Name: "Pricing", Present: deps.Pricing != nil && deps.Pricing.Store != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Service", Present: deps.Service != nil},
		httpio.Required{Name: "Service.Approvals", Present: deps.Service != nil && deps.Service.Approvals != nil},
		httpio.Required{Name: "Service.FileSummaries", Present: deps.Service != nil && deps.Service.FileSummaries != nil},
		httpio.Required{Name: "Service.Limits", Present: deps.Service != nil && deps.Service.Limits != nil},
		httpio.Required{Name: "Service.Power", Present: deps.Service != nil && deps.Service.Power != nil},
		httpio.Required{Name: "Service.Review", Present: deps.Service != nil && deps.Service.Review != nil},
		httpio.Required{Name: "Service.SecurityScanners", Present: deps.Service != nil && deps.Service.SecurityScanners != nil},
		httpio.Required{Name: "Service.Verify", Present: deps.Service != nil && deps.Service.Verify != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Sources.FileBriefings", Present: deps.Sources != nil && deps.Sources.FileBriefings != nil},
	)
	deps.Pricing.SetOnRefreshSettled(func() {
		projectview.PublishSettings(deps.Events, deps.Projects, context.Background(), wire.SettingsAreaPricing, "global", "", "refreshed")
	})
	return Handler{Deps: deps, responses: responses}
}
