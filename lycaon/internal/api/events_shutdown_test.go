package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An SSE handler is an active request that http.Server.Shutdown waits for
// without canceling, so the drain only completes if StopBackground releases
// the stream first.
func TestShutdownReleasesOpenEventStream(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	hub := events.NewMemoryHub()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "project.CreateWithRoot failed", err)

	memStore := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: memStore, Projects: reg, Sessions: session.NewManager(memStore, nil, nil, settings.DefaultSessionLimits()), Events: hub,
	}), nil, TestAPIToken)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	httpServer := &http.Server{Handler: srv}
	go func() { _ = httpServer.Serve(ln) }()
	defer httpServer.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://%s/v1/events?project_id=%s", ln.Addr().String(), p.ID), nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	WithTestAuth(req)

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

	srv.StopBackground()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown after StopBackground: %v (waited %s) — the open SSE stream held the drain", err, time.Since(started))
	}
}
