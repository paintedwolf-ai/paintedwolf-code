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
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A reader that holds a read's tail until more bytes arrive gets an event without waiting for the heartbeat.
func TestEventStreamEndsBurstWithComment(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	restore := events.SetHeartbeatIntervalForTests(time.Hour)
	defer restore()
	hub := events.NewMemoryHub()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "project.CreateWithRoot failed", err)

	memStore := store.NewMemory()
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: memStore, Projects: reg, Sessions: session.NewHost(memStore, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Host: hostapi.HostDependencies{Events: hub}}), nil, hostapi.TestAPIToken)

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

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	testutil.FailErr(t, "read stream preamble failed", err)
	if !strings.Contains(line, "connected") {
		t.Fatalf("stream preamble = %q, want a connected comment", line)
	}
	testutil.FailErr(t, "publish session event failed", hub.Publish(t.Context(), api.EventTopicSession, events.PublishKey{Project: p.ID}, api.SessionEvent{
		ID: "session-1", ProjectID: p.ID, Action: api.SessionEventActionUpdated, Status: api.SessionStatusIdle,
	}))

	var lines []string
	sawEvent := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		testutil.FailErr(t, "read stream failed", err)
		lines = append(lines, line)
		switch {
		case strings.HasPrefix(line, "data:"):
			sawEvent = true
		case line == strings.SplitAfter(events.BurstEndComment, "\n")[0]:
			if !sawEvent {
				t.Fatalf("the stream ended a burst before writing an event: %q", lines)
			}
			return
		}
	}
	t.Fatalf("no burst-end comment followed the event: %q", lines)
}
