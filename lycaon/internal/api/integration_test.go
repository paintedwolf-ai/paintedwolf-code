package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestServerEndToEnd(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	store := store.NewMemory()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}), nil, TestAPIToken)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpServer := &http.Server{Handler: srv}
	go httpServer.Serve(listener)
	t.Cleanup(func() {
		_ = httpServer.Close()
	})

	baseURL := fmt.Sprintf("http://%s", listener.Addr().String())

	healthReq, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/health", nil)
	resp, err := http.DefaultClient.Do(healthReq)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	openBody := fmt.Sprintf(`{"roots":[{"path":%q}]}`, dir)
	resp, err = authedHTTPPost(baseURL+"/v1/projects", "application/json", openBody)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", resp.StatusCode, string(readBody(t, resp)))
	}
	var opened wire.Project
	if err := json.NewDecoder(resp.Body).Decode(&opened); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	resp.Body.Close()

	createBody := fmt.Sprintf(`{"project_id":%q,"posture":"build"}`, opened.ID)
	resp, err = authedHTTPPost(baseURL+"/v1/sessions", "application/json", createBody)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", resp.StatusCode, string(readBody(t, resp)))
	}

	var sess wire.Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	resp.Body.Close()
	sess = waitSessionPrepared(t, baseURL, sess)

	resp, err = authedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = authedHTTPGet(baseURL + "/v1/projects")
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list projects status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	var before wire.Session
	resp, err = authedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
	if err != nil {
		t.Fatalf("get session before prompt: %v", err)
	}
	if err := json.NewDecoder(resp.Body).Decode(&before); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	resp.Body.Close()

	acceptPrompt(t, baseURL, sess.ID, "hello")
	_, streamURL := waitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)

	content, sawDone := readSSEStreamFromPath(t, baseURL, streamURL)
	if !sawDone {
		t.Fatal("expected stream done")
	}
	if !strings.Contains(content, "I understand") {
		t.Fatalf("stream content = %q", content)
	}

	resp, err = authedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
	if err != nil {
		t.Fatalf("get session after prompt: %v", err)
	}
	var after wire.Session
	if err := json.NewDecoder(resp.Body).Decode(&after); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	resp.Body.Close()
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatal("expected updated_at to advance after prompt")
	}
}

func readSSEStreamFromPath(t *testing.T, baseURL, streamPath string) (string, bool) {
	t.Helper()
	return readSSEStream(t, baseURL+streamPath)
}
