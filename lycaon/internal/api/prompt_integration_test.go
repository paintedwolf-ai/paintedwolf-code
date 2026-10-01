//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptEndToEnd(t *testing.T) {
	dir := t.TempDir()
	baseURL := startTestHTTPServer(t, newTestServerWithWorkflows(t))
	sess := createTestSession(t, baseURL, dir)

	acceptPrompt(t, baseURL, sess.ID, "hello")
	_, streamURL := waitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)

	content, sawDone := readSSEStream(t, baseURL+streamURL)
	if !sawDone {
		t.Fatal("expected done chunk")
	}
	if !strings.Contains(content, "I understand") {
		t.Fatalf("stream content = %q", content)
	}
}

func TestPromptDoesNotInventToolOutsideCurrentCapabilitySurface(t *testing.T) {
	dir := t.TempDir()
	reg := withoutTools(tools.NewStubRegistry(), "read")
	baseURL := startTestHTTPServer(t, newTestServerWithWorkflowRegistry(t, reg))
	sess := createTestSession(t, baseURL, dir)

	acceptPrompt(t, baseURL, sess.ID, "read the readme")
	_, streamURL := waitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)

	content, sawDone := readSSEStream(t, baseURL+streamURL)
	if !sawDone {
		t.Fatal("expected done chunk")
	}
	if content != "I understand. How can I help you further?" {
		t.Fatalf("stream content = %q", content)
	}
}

func TestSSEStream(t *testing.T) {
	dir := t.TempDir()
	baseURL := startTestHTTPServer(t, newTestServer(t))
	sess := createTestSession(t, baseURL, dir)

	acceptPrompt(t, baseURL, sess.ID, "hello")
	_, streamURL := waitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)
	content, sawDone := readSSEStream(t, baseURL+streamURL)
	if !sawDone {
		t.Fatal("expected done chunk")
	}
	want := "I understand. How can I help you further?"
	if content != want {
		t.Fatalf("stream content = %q, want %q", content, want)
	}
}

func TestCreateSessionByProjectIDPromptContext(t *testing.T) {
	dir := t.TempDir()
	baseURL := startTestHTTPServer(t, newTestServerWithWorkflows(t))

	openBody := fmt.Sprintf(`{"roots":[{"path":%q}]}`, dir)
	resp, err := authedHTTPPost(baseURL+"/v1/projects", "application/json", openBody)
	testutil.FailErr(t, "authedHTTPPost failed", err)
	var opened wire.Project
	json.NewDecoder(resp.Body).Decode(&opened)
	resp.Body.Close()

	createBody := fmt.Sprintf(`{"project_id":%q,"posture":"build"}`, opened.ID)
	resp, err = authedHTTPPost(baseURL+"/v1/sessions", "application/json", createBody)
	testutil.FailErr(t, "authedHTTPPost failed", err)
	var sess wire.Session
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	if sess.WorkspacePath != resolveTestPath(t, dir) {
		t.Fatalf("project_dir = %q", sess.WorkspacePath)
	}
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return getSessionAtURL(t, baseURL, sess.ID).Status != wire.SessionStatusPreparing
	})

	acceptPrompt(t, baseURL, sess.ID, "hello")
	waitForSessionIdle(t, baseURL, sess.ID, 10*time.Second)
}
