package searchadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
)

type Dependencies struct {
	Database         db.Handle
	Projects         project.Registry
	SessionStore     requestscope.Sessions
	Rerank           decide.Reranker
	EditorDocuments  *editordoc.Service
	SourceMutations  *projectsource.SourceMutationService
	ChatAffiliation  func(*http.Request) (string, int)
	WriteSourceError func(http.ResponseWriter, *http.Request, error)
}

type Handler struct {
	database         db.Handle
	projectRegistry  project.Registry
	sessionStore     requestscope.Sessions
	rerank           decide.Reranker
	editorDocuments  *editordoc.Service
	sourceMutations  *projectsource.SourceMutationService
	chatAffiliation  func(*http.Request) (string, int)
	writeSourceError func(http.ResponseWriter, *http.Request, error)
	responses        *httpio.Responder
	searchPages      *searchPageCache
	symbolExecutor   *sourceapi.SymbolExecutor
}

func New(responses *httpio.Responder, deps Dependencies) Handler {
	return Handler{database: deps.Database, projectRegistry: deps.Projects, sessionStore: deps.SessionStore, rerank: deps.Rerank,
		editorDocuments: deps.EditorDocuments, sourceMutations: deps.SourceMutations, chatAffiliation: deps.ChatAffiliation, writeSourceError: deps.WriteSourceError,
		responses: responses, searchPages: newSearchPageCache(), symbolExecutor: sourceapi.NewSymbolExecutor(deps.Projects)}
}
