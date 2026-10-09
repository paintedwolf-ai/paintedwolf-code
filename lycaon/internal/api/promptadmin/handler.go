package promptadmin

import (
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/visual"
)

// Deps are the prompt routes' dependencies, fixed at construction.
type Deps struct {
	DataDir        string
	EventPublisher *events.Publisher
	HintConfig     *guidance.HintConfig
	ManagedSecrets *secretcap.Service
	Projects       project.Registry
	Store          session.Store
	Sessions       *session.Host
	VisualStore    visual.Store
	Sources        *sourceapi.Handler
	// Video decodes attached videos; nil refuses them.
	Video promptattach.VideoDecoder
}

type Handler struct {
	Deps
	// Caps are the attachment limits active when the handler was built.
	Caps       promptattach.Caps
	responses  *httpio.Responder
	background *taskgroup.Group
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) Handler {
	httpio.RequireDependencies("promptadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "background", Present: background != nil},
		httpio.Required{Name: "EventPublisher", Present: deps.EventPublisher != nil},
		httpio.Required{Name: "ManagedSecrets", Present: deps.ManagedSecrets != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Sources", Present: deps.Sources != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "VisualStore", Present: deps.VisualStore != nil},
	)
	return Handler{Deps: deps, Caps: promptattach.Active(), responses: responses, background: background}
}
