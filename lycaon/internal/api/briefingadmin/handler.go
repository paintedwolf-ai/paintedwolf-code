package briefingadmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type Dependencies struct {
	EditorDocuments  *editordoc.Service
	FileBriefings    *filebriefing.Service
	ProjectRegistry  project.Registry
	SessionStore     session.Store
	SourceLedger     *sourceledger.Store
	WorkerBranchRoot func(context.Context, string, string) (string, func())
}
type Handler struct {
	EditorDocuments  *editordoc.Service
	FileBriefings    *filebriefing.Service
	ProjectRegistry  project.Registry
	SessionStore     session.Store
	SourceLedger     *sourceledger.Store
	workerBranchRoot func(context.Context, string, string) (string, func())
	responses        *httpio.Responder
}

func New(responses *httpio.Responder, deps Dependencies) *Handler {
	return &Handler{EditorDocuments: deps.EditorDocuments, FileBriefings: deps.FileBriefings, ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, workerBranchRoot: deps.WorkerBranchRoot, responses: responses}
}
