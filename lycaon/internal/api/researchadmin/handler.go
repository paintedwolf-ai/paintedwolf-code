// Package researchadmin serves web research settings, credentials, diagnostics, and index status.
package researchadmin

import (
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/internal/webresearch"
)

// Deps are the web research routes' dependencies, fixed at construction.
type Deps struct {
	// Runtime holds the provider catalog, registry, config, and credential vault.
	Runtime webresearch.Runtime
	// Discoverer runs direct search for provider tests.
	Discoverer webresearch.DirectDiscovererFactory
	// Index is the persistent web index; nil when it could not be opened at boot.
	Index  *webindex.Store
	Events events.ReplayHub
}

type Handler struct {
	Deps
	responses *httpio.Responder
}

func New(responses *httpio.Responder, deps Deps) *Handler {
	httpio.RequireDependencies("researchadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "Runtime.Catalog", Present: deps.Runtime.Catalog != nil},
		httpio.Required{Name: "Runtime.Config", Present: deps.Runtime.Config != nil},
		httpio.Required{Name: "Runtime.Creds", Present: deps.Runtime.Creds != nil},
		httpio.Required{Name: "Runtime.Registry", Present: deps.Runtime.Registry != nil},
		httpio.Required{Name: "Discoverer", Present: deps.Discoverer != nil},
		httpio.Required{Name: "Events", Present: deps.Events != nil},
	)
	return &Handler{Deps: deps, responses: responses}
}
