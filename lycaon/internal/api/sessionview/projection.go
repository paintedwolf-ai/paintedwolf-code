package sessionview

import (
	"context"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type Projector struct {
	Workflows *workflow.RunManager
	Store     session.Store
	Sessions  *session.Manager
	Projects  project.Registry
}

// New validates the projector's dependencies.
func New(p Projector) Projector {
	httpio.RequireDependencies("sessionview",
		httpio.Required{Name: "Projects", Present: p.Projects != nil},
		httpio.Required{Name: "Sessions", Present: p.Sessions != nil},
		httpio.Required{Name: "Store", Present: p.Store != nil},
		httpio.Required{Name: "Workflows", Present: p.Workflows != nil},
	)
	return p
}

func (s *Projector) EnrichSession(ctx context.Context, sess *wire.Session) {
	if sess == nil {
		return
	}
	s.HydrateSessionWorkspace(ctx, sess)
	sess.UntrustedContent = s.Store.SessionUntrustedContent(sess.ID)
	sess.PromptPending = s.Sessions.Runner.SubmissionState.PromptPending(ctx, sess.ID)
	if ui, err := s.Workflows.ComputeSessionUI(ctx, sess.ID); err == nil {
		sess.UI = ui
	}
	if protection := s.Sessions.Protection.ProtectionStateForSession(sess.ID); protection != nil {
		if sess.UI == nil {
			sess.UI = &wire.SessionUiState{}
		}
		sess.UI.Protection = protection
	}
}

func (s *Projector) HydrateSessionWorkspace(ctx context.Context, sess *wire.Session) {
	if sess == nil || strings.TrimSpace(sess.WorkspacePath) != "" || strings.TrimSpace(sess.ProjectID) == "" {
		return
	}
	p, err := s.Projects.Get(ctx, sess.ProjectID)
	if err != nil || len(p.Roots) == 0 {
		return
	}
	rootID, path, err := project.ResolveWorkspaceRoot(p, sess.WorkspaceRootID)
	if err != nil {
		return
	}
	if rootID != "" {
		sess.WorkspaceRootID = rootID
	}
	sess.WorkspacePath = path
}

func (s *Projector) EnrichWorkflowRun(ctx context.Context, run *wire.WorkflowRun) {
	if run == nil {
		return
	}
	_ = s.Workflows.AttachRunUI(ctx, run)
}

func (s *Projector) EnrichWorkflowRuns(ctx context.Context, runs []*wire.WorkflowRun) {
	for _, run := range runs {
		_ = s.Workflows.AttachRunUI(ctx, run)
	}
}

func (s *Projector) WriteWorkflowRun(w http.ResponseWriter, r *http.Request, status int, run *wire.WorkflowRun) {
	s.EnrichWorkflowRun(r.Context(), run)
	httpio.WriteJSON(w, status, run)
}
