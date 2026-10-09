package editoradmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type Dependencies struct {
	EditorClients   *editordoc.ClientLiveness
	EditorDocuments *editordoc.Service
	Events          events.ReplayHub
	ManagedSecrets  *secretcap.Service
	MutationGate    *project.MutationGate
	ProjectRegistry project.Registry
	SecretSpans     *secretspan.Screener
	SessionStore    session.Store
	TryRunPromotion func(context.Context, string)
}
type Handler struct {
	EditorClients   *editordoc.ClientLiveness
	EditorDocuments *editordoc.Service
	Events          events.ReplayHub
	ManagedSecrets  *secretcap.Service
	MutationGate    *project.MutationGate
	ProjectRegistry project.Registry
	SecretSpans     *secretspan.Screener
	SessionStore    session.Store
	TryRunPromotion func(context.Context, string)
	editorScreens   editorScreenMemo
	background      *taskgroup.Group
	responses       *httpio.Responder
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Dependencies) *Handler {
	return &Handler{EditorClients: deps.EditorClients, EditorDocuments: deps.EditorDocuments, Events: deps.Events, ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate, ProjectRegistry: deps.ProjectRegistry, SecretSpans: deps.SecretSpans, SessionStore: deps.SessionStore, TryRunPromotion: deps.TryRunPromotion, responses: responses, background: background}
}
func (s *Handler) EditorScreen(documentID, digest string) (*wire.SecretScreen, bool) {
	return s.editorScreens.get(documentID, digest)
}
