package sessioncontracts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCreateSessionReturnsBeforeInventoryAndDetachesCancellation(t *testing.T) {
	// The project is created on the registry, not through the server: project
	// creation schedules its own inventory, and the fake must see only the
	// session's.
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	registry := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	fake := &contractfixture.BlockingInventoryService{
		Started: make(chan struct{}), Release: make(chan struct{}), CtxErr: make(chan error, 1),
	}
	srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) {
		d.Core.Projects = registry
		d.Source.SourceInventory = fake
	})

	ctx, cancel := context.WithCancel(t.Context())
	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session creation waited for background source inventory")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.ID == "" || sess.Status != wire.SessionStatusPreparing {
		t.Fatalf("session = %+v, want durable preparing row", sess)
	}

	select {
	case <-fake.Started:
	case <-time.After(time.Second):
		t.Fatal("source inventory was not scheduled")
	}
	cancel()
	close(fake.Release)
	select {
	case err := <-fake.CtxErr:
		if err != nil {
			t.Fatalf("inventory inherited request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source inventory did not finish")
	}
	contractfixture.DrainBackground(t, srv)
}
