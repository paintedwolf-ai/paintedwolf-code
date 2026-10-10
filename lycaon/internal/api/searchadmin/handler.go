package searchadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
)

type Dependencies struct {
	Database         db.Handle
	Projects         project.Registry
	Rerank           decide.Reranker
	SourceMutations  *projectsource.SourceMutationService
	ChatAffiliation  func(*http.Request) (string, int)
	WriteSourceError func(http.ResponseWriter, *http.Request, error)
}

type Handler struct {
	database         db.Handle
	projectRegistry  project.Registry
	rerank           decide.Reranker
	sourceMutations  *projectsource.SourceMutationService
	chatAffiliation  func(*http.Request) (string, int)
	writeSourceError func(http.ResponseWriter, *http.Request, error)
	responses        *httpio.Responder
	searchPages      *searchPageCache
}

func New(responses *httpio.Responder, deps Dependencies) Handler {
	return Handler{database: deps.Database, projectRegistry: deps.Projects, rerank: deps.Rerank,
		sourceMutations: deps.SourceMutations, chatAffiliation: deps.ChatAffiliation, writeSourceError: deps.WriteSourceError,
		responses: responses, searchPages: newSearchPageCache()}
}
