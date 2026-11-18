package hostcontracts

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestShutdownReleasesOpenEventStream(t *testing.T) {
	sessionFixture := storetest.New(t)

	hub := events.NewMemoryHub()
	reg := project.NewSQLRegistry(sessionFixture.DB())
	p, err := projecttest.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "project.CreateWithRoot failed", err)

	sessionStore := sessionFixture
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionStore, Projects: reg, Sessions: session.NewHost(sessionStore, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Host: hostapi.HostDependencies{Events: hub}}), nil, hostapi.TestAPIToken)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	httpServer := &http.Server{Handler: srv}
	go func() { _ = httpServer.Serve(ln) }()
	defer httpServer.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://%s/v1/events?project_id=%s", ln.Addr().String(), p.ID), nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	hostapi.WithTestAuth(req)

	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()

	// The preamble proves the handler reached its event loop, so the stream is
	// in Shutdown's active-request set.
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	testutil.FailErr(t, "read stream preamble failed", err)
	if !strings.Contains(line, "connected") {
		t.Fatalf("stream preamble = %q, want a connected comment", line)
	}

	srv.StopBackground(t.Context())

	shutdownCtx := t.Context()
	started := time.Now()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown after StopBackground: %v (waited %s) — the open SSE stream held the drain", err, time.Since(started))
	}
}
