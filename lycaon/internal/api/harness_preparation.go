package api

import (
	"context"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/upgradefixture"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) registerHarnessPreparation(r chi.Router) {
	r.Get("/contract", s.handleHarnessContract)
	r.Get("/execution/{sessionID}/{submissionID}", s.handleHarnessExecution)
	r.Get("/workflow-execution/{sessionID}/{runID}", s.handleHarnessWorkflowExecution)
	r.Post("/model-limit", s.handleHarnessModelLimit)
	r.Post("/write-resource", s.handleHarnessWriteResource)
	r.Post("/preparation", s.handleHarnessPreparation)
	r.Get("/preparation/{sessionID}", s.handleHarnessPreparationReceipt)
}

// harnessServices are what the harness build adds to the host, validated when
// the harness routes register.
type harnessServices struct {
	seeder      untrustedContentSeeder
	database    db.Handle
	preparation *harnessfixture.PreludeController
}

type untrustedContentSeeder interface {
	SeedUntrustedContent(context.Context, string) error
}

func (s *Server) requireHarnessServices() harnessServices {
	seeder, _ := s.sessionStore.(untrustedContentSeeder)
	preparation, _ := s.llmSvc.Preparation.(*harnessfixture.PreludeController)
	httpio.RequireDependencies("harness",
		httpio.Required{Name: "HarnessWorkers", Present: s.harnessWorkers != nil},
		httpio.Required{Name: "LLM.Preparation", Present: preparation != nil},
		httpio.Required{Name: "Store.SeedUntrustedContent", Present: seeder != nil},
	)
	return harnessServices{seeder: seeder, database: s.database, preparation: preparation}
}

func (s *Server) handleHarnessPreparation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string                 `json:"session_id"`
		Prelude   harnessfixture.Prelude `json:"prelude"`
	}
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !requestscope.SessionExists(s.sessionStore, &s.responses, w, r, request.SessionID) {
		return
	}
	if err := s.harness.preparation.Install(r.Context(), request.SessionID, request.Prelude); err != nil {
		s.harnessRequestRejected(w, r, "the preparation prelude could not be installed", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{"operation_id": request.Prelude.OperationID})
}

func (s *Server) handleHarnessPreparationReceipt(w http.ResponseWriter, r *http.Request) {
	receipt, err := s.harness.preparation.Receipt(chi.URLParam(r, "sessionID"))
	if os.IsNotExist(err) {
		s.responses.Fail(w, wire.ApiErrorCodePreparationReceiptNotFound, "Preparation has not reached candidate entry")
		return
	}
	if err != nil {
		s.harnessRequestRejected(w, r, "the preparation receipt could not be read", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, receipt)
}

func (s *Server) handleHarnessWriteResource(w http.ResponseWriter, r *http.Request) {
	var request harnessfixture.WriteResourceRequest
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	resource, err := harnessfixture.PrepareWriteResource(request)
	if err != nil {
		s.harnessRequestRejected(w, r, "the write resource could not be prepared", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, resource)
}

func (s *Server) handleHarnessContract(w http.ResponseWriter, _ *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, map[string]any{
		"application_version": version.Version,
		"profile":             "development-harness",
		"contract":            harnessfixture.Contract(),
	})
}

func (s *Server) handleHarnessUpgradeHistory(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string `json:"session_id"`
	}
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	parent, ok := requestscope.Session(s.sessionStore, &s.responses, w, r, request.SessionID)
	if !ok {
		return
	}
	project, err := s.projectRegistry.Get(r.Context(), parent.ProjectID)
	if err != nil || project == nil || len(project.Roots) != 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Fixture requires one attached project root")
		return
	}
	evidence, err := upgradefixture.SeedSourceHistory(r.Context(), s.harness.database, s.dataDir, project.Roots[0].Path, project.ID, parent.ID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, evidence)
}
