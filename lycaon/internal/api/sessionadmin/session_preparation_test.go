package sessionadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/contextpreparation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionSeenOffersPreparationAfterSuccessfulLookup(t *testing.T) {
	deps := apitestdeps.Deps{}
	apitestdeps.Fill(t, &deps)
	proj, err := project.CreateWithRoot(t.Context(), deps.Projects, t.TempDir())
	testutil.FailErr(t, "create preparation project", err)
	sess, err := deps.Store.CreateWithStatus(t.Context(), api.CreateSessionRequest{ProjectID: proj.ID}, proj.ID, api.SessionStatusIdle)
	testutil.FailErr(t, "create preparation chat", err)
	offered := make(chan string, 1)
	preparation := contextpreparation.New(func(ctx context.Context, id string) (contextpreparation.Candidate, error) {
		offered <- id
		<-ctx.Done()
		return contextpreparation.Candidate{}, ctx.Err()
	})
	deps.Sessions.ContextPreparation = preparation
	t.Cleanup(func() {
		preparation.Stop()
		testutil.FailErr(t, "join preparation", preparation.Wait(context.Background()))
	})
	handler := &Transcript{Store: deps.Store, Sessions: deps.Sessions, Projects: deps.Projects, responses: &httpio.Responder{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/seen", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", sess.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.HandleMarkSessionSeen(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("mark seen: %d %s", recorder.Code, recorder.Body.String())
	}
	if id := <-offered; id != sess.ID {
		t.Fatalf("preparation chat = %s", id)
	}
}
