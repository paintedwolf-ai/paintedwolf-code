package promptadmin

import (
	"github.com/lycaon/lycaon/internal/api/editoradmin"
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
	Attachments *Attachments
	Content     *Content
	Execution   *Execution
	Queue       *Queue
	References  *References
	Submission  *Submission
}

type Attachments struct {
	Caps       promptattach.Caps
	DataDir    string
	Projects   project.Registry
	Sessions   *session.Host
	Store      session.Store
	Submission *Submission
	Video      promptattach.VideoDecoder
	responses  *httpio.Responder
}

type Content struct {
	HintConfig *guidance.HintConfig
	Sessions   *session.Host
	Store      session.Store
	responses  *httpio.Responder
}

type Execution struct {
	Attachments    *Attachments
	EventPublisher *events.Publisher
	Sessions       *session.Host
	Store          session.Store
	background     *taskgroup.Group
	responses      *httpio.Responder
}

type Queue struct {
	Sessions   *session.Host
	Store      session.Store
	background *taskgroup.Group
	responses  *httpio.Responder
}

type References struct {
	Caps         promptattach.Caps
	Projects     project.Registry
	Store        session.Store
	VisualStore  visual.Store
	sourceEditor *editoradmin.Handler
}

type Submission struct {
	Attachments    *Attachments
	Caps           promptattach.Caps
	EventPublisher *events.Publisher
	Execution      *Execution
	ManagedSecrets *secretcap.Service
	References     *References
	Sessions       *session.Host
	Store          session.Store
	Video          promptattach.VideoDecoder
	VisualStore    visual.Store
	responses      *httpio.Responder
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
	caps := promptattach.Active()
	h := Handler{}
	h.Attachments = &Attachments{Caps: caps, DataDir: deps.DataDir, Projects: deps.Projects, Sessions: deps.Sessions, Store: deps.Store, Video: deps.Video, responses: responses}
	h.Content = &Content{HintConfig: deps.HintConfig, Sessions: deps.Sessions, Store: deps.Store, responses: responses}
	h.Execution = &Execution{EventPublisher: deps.EventPublisher, Sessions: deps.Sessions, Store: deps.Store, background: background, responses: responses}
	h.Queue = &Queue{Sessions: deps.Sessions, Store: deps.Store, background: background, responses: responses}
	h.References = &References{Caps: caps, Projects: deps.Projects, Store: deps.Store, VisualStore: deps.VisualStore, sourceEditor: deps.Sources.Editor}
	h.Submission = &Submission{Caps: caps, EventPublisher: deps.EventPublisher, ManagedSecrets: deps.ManagedSecrets, Sessions: deps.Sessions, Store: deps.Store, Video: deps.Video, VisualStore: deps.VisualStore, responses: responses}
	h.Attachments.Submission = h.Submission
	h.Execution.Attachments = h.Attachments
	h.Submission.Attachments = h.Attachments
	h.Submission.Execution = h.Execution
	h.Submission.References = h.References
	return h
}
