package contract

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func newRealServerWithSession(t *testing.T, root string) (*api.Server, string) {
	sessionFixture := storetest.New(t, testdbseed.DefaultProjectID)

	t.Helper()
	store := sessionFixture
	mockCfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(mockCfg), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core: api.CoreDependencies{Store: store, Projects: project.NewSQLRegistry(sessionFixture.DB()), Sessions: mgr}}), nil, api.TestAPIToken)

	// Wait for detached prompt work before temporary-directory cleanup.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.WaitForBackground(ctx)
	})
	sess, err := store.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return srv, sess.ID
}

type responseRecorder struct {
	code   int
	header http.Header
	body   strings.Builder
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(statusCode int) { r.code = statusCode }

func (r *responseRecorder) Flush() {}
