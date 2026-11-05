package projectadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
)

type backgroundRegistry struct {
	project.Registry
	err error
}

func (r backgroundRegistry) Get(_ context.Context, id string) (*project.Project, error) {
	return &project.Project{ID: id}, r.err
}

func backgroundRequest(id, process string) *http.Request {
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	route.URLParams.Add("process_id", process)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
}

func TestBackgroundHandlersRefuseBeforeReachingProcesses(t *testing.T) {
	t.Parallel()
	missing := &Projects{Registry: backgroundRegistry{err: project.ErrNotFound}, responses: &httpio.Responder{}}
	for name, handle := range map[string]http.HandlerFunc{
		"list": missing.HandleListProjectBackgroundProcesses,
		"stop": missing.HandleStopProjectBackgroundProcess,
	} {
		rec := httptest.NewRecorder()
		handle(rec, backgroundRequest("p", "job"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s on a missing project = %d", name, rec.Code)
		}
	}
	present := &Projects{Registry: backgroundRegistry{}, responses: &httpio.Responder{}}
	rec := httptest.NewRecorder()
	present.HandleStopProjectBackgroundProcess(rec, backgroundRequest("p", " "))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stop without a process id = %d", rec.Code)
	}
}
