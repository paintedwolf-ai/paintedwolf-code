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
	Bootstrap  *Bootstrap
	Lifecycle  *Lifecycle
	Navigation *Navigation
	Recovery   *Recovery
	Rewind     *Rewind
	Transcript *Transcript
}

type Bootstrap struct {
	Checkpoints    hitl.CheckpointManager
	EventPublisher *events.Publisher
	Events         events.ReplayHub
	Preview        *preview.Controller
	ProgressStore  progress.Store
	SessionView    *sessionview.Projector
	Sessions       *session.Manager
	Store          session.Store
	Workers        worker.WorkerQueue
	responses      *httpio.Responder
}

type Lifecycle struct {
	Events          events.ReplayHub
	FileAgeWarmer   func(ctx context.Context, projectDir string)
	Git             *gitadmin.Handler
	LLMService      *llm.Service
	ProjectRules    *rules.ProjectRulesOverlay
	Projects        project.Registry
	Prompt          *promptadmin.Queue
	SessionView     *sessionview.Projector
	Sessions        *session.Manager
	Settings        *settings.Service
	Store           session.Store
	Workflows       *workflow.RunManager
	background      *taskgroup.Group
	responses       *httpio.Responder
	sourceWatch     *sourceapi.Watch
	sourceWorkspace *sourceapi.Workspace
}

type Navigation struct {
	Sessions        *session.Manager
	Store           session.Store
	Workers         worker.WorkerQueue
	responses       *httpio.Responder
	sourceWorkspace *sourceapi.Workspace
}

type Recovery struct {
	Sessions  *session.Manager
	responses *httpio.Responder
}

type Rewind struct {
	Sessions  *session.Manager
	responses *httpio.Responder
}

type Transcript struct {
	Events      events.ReplayHub
	Invocations invocation.Recorder
	Projects    project.Registry
	Sessions    *session.Manager
	Store       session.Store
	responses   *httpio.Responder
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) Handler {
	httpio.RequireDependencies("sessionadmin",
		httpio.Required{Name: "Invocations", Present: deps.Invocations != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "Workflows", Present: deps.Workflows != nil},
	)
	h := Handler{}
	h.Bootstrap = &Bootstrap{Checkpoints: deps.Checkpoints, EventPublisher: deps.EventPublisher, Events: deps.Events, Preview: deps.Preview, ProgressStore: deps.ProgressStore, SessionView: deps.SessionView, Sessions: deps.Sessions, Store: deps.Store, Workers: deps.Workers, responses: responses}
	h.Lifecycle = &Lifecycle{Events: deps.Events, FileAgeWarmer: deps.FileAgeWarmer, Git: deps.Git, LLMService: deps.LLMService, ProjectRules: deps.ProjectRules, Projects: deps.Projects, Prompt: deps.Prompt.Queue, SessionView: deps.SessionView, Sessions: deps.Sessions, Settings: deps.Settings, Store: deps.Store, Workflows: deps.Workflows, background: background, responses: responses, sourceWatch: deps.Sources.Watch, sourceWorkspace: deps.Sources.Workspace}
	h.Navigation = &Navigation{Sessions: deps.Sessions, Store: deps.Store, Workers: deps.Workers, responses: responses, sourceWorkspace: deps.Sources.Workspace}
	h.Recovery = &Recovery{Sessions: deps.Sessions, responses: responses}
	h.Rewind = &Rewind{Sessions: deps.Sessions, responses: responses}
	h.Transcript = &Transcript{Events: deps.Events, Invocations: deps.Invocations, Projects: deps.Projects, Sessions: deps.Sessions, Store: deps.Store, responses: responses}
	return h
}
