//go:build integration

package hostcontracts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSSEEndToEnd(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	hub := events.NewMemoryHub()
	reg := project.NewMemoryRegistry()
	ctx := context.Background()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)

	store := store.NewMemory()
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: reg, Sessions: session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Host: hostapi.HostDependencies{Events: hub}}), nil, hostapi.TestAPIToken)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	defer ln.Close()

	httpServer := &http.Server{Handler: srv}
	go httpServer.Serve(ln)
	defer httpServer.Close()

	baseURL := fmt.Sprintf("http://%s", ln.Addr().String())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/events?project_id="+p.ID, nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	hostapi.WithTestAuth(req)

	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q want text/event-stream", ct)
	}

	_ = hub.Publish(ctx, api.EventTopicSession, events.PublishKey{Project: p.ID}, api.SessionEvent{
		ID:        "session-1",
		ProjectID: p.ID,
		Action:    api.SessionEventActionUpdated,
		Status:    api.SessionStatusIdle,
	})

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.HasPrefix(line, "data:") {
			if !strings.Contains(line, "session-1") {
				t.Fatalf("data line missing session id: %q", line)
			}
			return
		}
	}
	t.Fatal("timeout waiting for SSE data line")
}

func TestSSEDeviceWideSubscription(t *testing.T) {
	hub := events.NewMemoryHub()
	sessionStore := store.NewMemory()
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store:    sessionStore,
		Projects: project.NewMemoryRegistry(),
		Sessions: session.NewHost(sessionStore, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Host: hostapi.HostDependencies{
		Events: hub}}), nil, hostapi.TestAPIToken)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	defer ln.Close()

	httpServer := &http.Server{Handler: srv}
	go httpServer.Serve(ln)
	defer httpServer.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://%s/v1/events", ln.Addr()), nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	hostapi.WithTestAuth(req)

	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	testutil.FailErr(t, "read connected event", err)
	if line != ": connected\n" {
		t.Fatalf("connected event = %q", line)
	}
}

func TestSSERejectsEmptyProjectQuery(t *testing.T) {
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store:    store.NewMemory(),
		Projects: project.NewMemoryRegistry()}, Host: hostapi.HostDependencies{
		Events: events.NewMemoryHub()}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/events?project_id=", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", w.Code, http.StatusBadRequest)
	}
}

func TestSSEStreamEndReleasesTheClientsLeases(t *testing.T) {
	released := make(chan string, 1)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store.NewMemory(), Projects: project.NewMemoryRegistry()}, Host: hostapi.HostDependencies{Events: events.NewMemoryHub()}, Source: hostapi.SourceDependencies{
		EditorClients: editordoc.NewClientLiveness(time.Millisecond, func(clientID string) { released <- clientID })}}), nil, hostapi.TestAPIToken)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	defer ln.Close()
	httpServer := &http.Server{Handler: srv}
	go httpServer.Serve(ln)
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/v1/events?client_id=window-a", ln.Addr()), nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()
	// The connected comment means the handler registered this stream.
	_, err = bufio.NewReader(resp.Body).ReadString('\n')
	testutil.FailErr(t, "read connected comment", err)

	cancel()

	select {
	case client := <-released:
		if client != "window-a" {
			t.Fatalf("released leases for %q, want window-a", client)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a client whose only stream ended kept its editing leases")
	}
}

func TestSSERejectsEmptyClientQuery(t *testing.T) {
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store:    store.NewMemory(),
		Projects: project.NewMemoryRegistry()}, Host: hostapi.HostDependencies{
		Events: events.NewMemoryHub()}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/events?client_id=", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", w.Code, http.StatusBadRequest)
	}
}

func TestSSEReplayErrorCarriesRecoveryBoundary(t *testing.T) {
	hub := events.NewMemoryHub()
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store.NewMemory(), Projects: project.NewMemoryRegistry()}, Host: hostapi.HostDependencies{Events: hub}}), nil, hostapi.TestAPIToken)
	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/events?after=invalid", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want %d", w.Code, http.StatusConflict)
	}
	var response api.ErrorResponse
	testutil.FailErr(t, "decode replay error", json.Unmarshal(w.Body.Bytes(), &response))
	if response.Code != "event_replay_unavailable" {
		t.Fatalf("code=%q", response.Code)
	}
	cursor, ok := response.Details["event_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("event_cursor=%#v", response.Details["event_cursor"])
	}
}

func TestSSEHeartbeat(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	restore := events.SetHeartbeatIntervalForTests(50 * time.Millisecond)
	defer restore()

	hub := events.NewMemoryHub()
	reg := project.NewMemoryRegistry()
	ctx := context.Background()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)

	store := store.NewMemory()
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: reg, Sessions: session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Host: hostapi.HostDependencies{Events: hub}}), nil, hostapi.TestAPIToken)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	defer ln.Close()

	httpServer := &http.Server{Handler: srv}
	go httpServer.Serve(ln)
	defer httpServer.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://%s/v1/events?project_id=%s", ln.Addr(), p.ID), nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	hostapi.WithTestAuth(req)

	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(line, "ping") {
			return
		}
	}
	t.Fatal("timeout waiting for heartbeat ping")
}
