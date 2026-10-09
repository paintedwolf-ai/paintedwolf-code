package contractfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func DecodeAgentPresence(t *testing.T, rec *httptest.ResponseRecorder) wire.AgentPresenceSnapshot {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got wire.AgentPresenceSnapshot
	testutil.FailErr(t, "decode presence", json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func GetAgentPresence(t *testing.T, srv *hostapi.Server, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, NewAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/agent-presence", nil))
	return rec
}

func NewPresenceServer(t *testing.T) (*hostapi.Server, *agentpresence.Tracker, *PresenceTestAnchors, wire.Project) {
	t.Helper()
	chats := PresenceTestChats{}
	anchors := &PresenceTestAnchors{hold: false}
	tracker := agentpresence.New(chats, nil)
	tracker.SetAnchors(anchors)
	srv := NewTestServer(t, func(d *hostapi.Dependencies) { d.Source.AgentPresence = tracker })
	p := CreateProjectForTest(t, srv, t.TempDir())
	chats["chat-1"] = agentpresence.ChatRef{ProjectID: p.ID, SessionID: "chat-1", Title: "Config defaults"}
	tracker.ReadsReturned(t.Context(), agentpresence.Call{SessionID: "chat-1", ToolCallID: "call-1", Tool: "read"}, []agentpresence.Read{{
		Target:   agentpresence.Target{RootID: p.Roots[0].ID, Path: "pkg/a.go"},
		Document: agentpresence.Document{ID: "doc-a", Revision: 4},
		Extent:   wire.AgentPresenceExtentRange,
		Spans:    []agentpresence.Span{{StartLine: 3, EndLine: 9}},
	}})
	tracker.Settle()
	return srv, tracker, anchors, p
}

func PresenceRequest(t *testing.T, srv *hostapi.Server, method, path string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	hostapi.WithTestAuth(req)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("%s %s = 401; the request never reached the presence middleware", method, path)
	}
}

// Background polling and streaming do not establish user activity.

type PresenceTestAnchors struct{ hold bool }

func (a *PresenceTestAnchors) AnchorSpans(_ context.Context, _, documentID string, revision int64, spans []agentpresence.Span) (agentpresence.Anchored, error) {
	out := agentpresence.Anchored{DocumentID: documentID, Epoch: 2, Revision: revision}
	for _, span := range spans {
		out.Spans = append(out.Spans, agentpresence.AnchoredSpan{
			Anchor: fmt.Appendf(nil, "anchor:%d", span.StartLine), Head: fmt.Appendf(nil, "head:%d", span.EndLine), Expected: "text", Checkable: true,
		})
	}
	return out, nil
}

func (a *PresenceTestAnchors) AnchorPathSpans(context.Context, string, agentpresence.Target, []agentpresence.Span) (agentpresence.Anchored, bool, error) {
	return agentpresence.Anchored{}, false, nil
}

func (a *PresenceTestAnchors) SpansHold(_ context.Context, _, _ string, spans []agentpresence.AnchoredSpan) ([]bool, error) {
	out := make([]bool, len(spans))
	for i := range out {
		out[i] = a.hold
	}
	return out, nil
}

// newPresenceServer installs a tracker with one read by chat-1 in the project.

type PresenceTestChats map[string]agentpresence.ChatRef

func (c PresenceTestChats) Chat(_ context.Context, sessionID string) (agentpresence.ChatRef, bool) {
	ref, ok := c[sessionID]
	return ref, ok
}

// presenceTestAnchors anchors every span and reports whether spans still hold.

func PresenceTestServer(t *testing.T) (*hostapi.Server, *events.Presence) {
	t.Helper()
	hub := events.NewMemoryHub()
	st := store.NewMemory()
	p := events.NewPresence(hub, time.Minute)
	srv := hostapi.NewServer(RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: st, Projects: project.NewMemoryRegistry(), Sessions: session.NewManager(st, nil, nil, settings.DefaultSessionLimits())}, Host: hostapi.HostDependencies{
		Events: hub, Presence: p}}), nil, hostapi.TestAPIToken)
	return srv, p
}

// presenceRequest issues an authenticated request and fails if auth rejected
// it — an unauthenticated call never reaches the presence middleware, which
// would make "no stamp" assertions pass for the wrong reason.
