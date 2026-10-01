package sessionadmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
)

// Deps are the session routes' dependencies, fixed at construction.
type Deps struct {
	Checkpoints    hitl.CheckpointManager
	EventPublisher *events.Publisher
	Events         events.ReplayHub
	// FileAgeWarmer primes file-age facts for a project a new session opens.
	FileAgeWarmer func(ctx context.Context, projectDir string)
	Invocations   invocation.Recorder
	LLMService    *llm.Service
	Preview       *preview.Controller
	ProgressStore progress.Store
	Projects      project.Registry
	ProjectRules  *rules.ProjectRulesOverlay
	Store         session.Store
	Sessions      *session.Manager
	Workers       worker.WorkerQueue
	Workflows     *workflow.RunManager
	Settings      *settings.Service
	Sources       *sourceapi.Handler
	SessionView   *sessionview.Projector
	Git           *gitadmin.Handler
	Prompt        *promptadmin.Handler
}

type Handler struct {
	Deps
	responses  *httpio.Responder
	background *taskgroup.Group
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) Handler {
	httpio.RequireDependencies("sessionadmin",
		httpio.Required{Name: "Invocations", Present: deps.Invocations != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "Workflows", Present: deps.Workflows != nil},
	)
	return Handler{Deps: deps, responses: responses, background: background}
}
