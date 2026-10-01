package smoke_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// smokeModuleRoot and sharedServeBin are populated once by TestMain: the
// module root the serve binary is built and run from, and the path to that
// binary. The go build is the slowest part of the suite, so every smoke test
// shares one binary instead of rebuilding.
var (
	smokeModuleRoot string
	sharedServeBin  string
)

func TestMain(m *testing.M) {
	os.Exit(runSmokeTests(m))
}

func runSmokeTests(m *testing.M) int {
	testing.Init()
	flag.Parse()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "runtime.Caller failed")
		return 1
	}
	smokeModuleRoot = filepath.Join(filepath.Dir(file), "..", "..")

	// Build the serve binary once and share it. The smoke tests that use it are
	// skipped in short mode, so skip the build there too.
	if !testing.Short() {
		dir, err := os.MkdirTemp("", "lycaon-smoke-bin-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mkdir temp: %v\n", err)
			return 1
		}
		defer os.RemoveAll(dir)
		sharedServeBin = filepath.Join(dir, "lycaon")
		build := exec.Command("go", "build", "-o", sharedServeBin, "./cmd/lycaon")
		build.Dir = smokeModuleRoot
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build serve binary: %v\n%s", err, out)
			return 1
		}
	}
	return m.Run()
}

func TestLycaonServeSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}

	moduleRoot := smokeModuleRoot
	bin := sharedServeBin

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	apiToken := "lycaon-smoke-test-token"
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "smoke.db")
	proc := exec.CommandContext(t.Context(), bin, "serve", "--db", dbPath)
	proc.Dir = moduleRoot
	proc.Env = append(os.Environ(),
		"LYCAON_ADDR="+addr,
		"LYCAON_API_TOKEN="+apiToken,
		"LYCAON_LLM_MOCK=1",
	)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	})

	baseURL := "http://" + addr
	waitForHealth(t, baseURL)

	unauthReq, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/projects", nil)
	unauth, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauth probe: %v", err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /v1/projects status = %d, want 401", unauth.StatusCode)
	}

	sessionID := createSmokeSession(t, baseURL, apiToken, createSmokeProject(t, baseURL, apiToken, dir))

	operationID := uuid.NewString()
	promptReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/sessions/"+sessionID+"/prompts",
		strings.NewReader(fmt.Sprintf(`{"operation_id":%q,"text":"hello"}`, operationID)))
	if err != nil {
		t.Fatalf("prompt request: %v", err)
	}
	promptReq.Header.Set("Content-Type", "application/json")
	promptReq.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(promptReq)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt status = %d", resp.StatusCode)
	}
	var accepted api.PromptAcceptedResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode prompt accept: %v", err)
	}

	assertSmokePromptIdentity(t, accepted, operationID)
	waitSmokePromptComplete(t, baseURL, apiToken, sessionID, accepted.MessageID, "hello", 30*time.Second)
	assistantID := waitSmokeAssistantMessage(t, baseURL, apiToken, sessionID, 5*time.Second)

	streamReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/stream?message="+assistantID, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	streamReq.Header.Set("Authorization", "Bearer "+apiToken)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", streamResp.StatusCode)
	}
	if ct := streamResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
}

func waitForHealth(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/health", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become healthy", baseURL)
}

func createSmokeProject(t *testing.T, baseURL, apiToken, projectDir string) string {
	t.Helper()
	body := fmt.Sprintf(`{"roots":[{"path":%q}]}`, projectDir)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/projects", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create project request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create project status = %d body = %s", resp.StatusCode, respBody)
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if project.ID == "" {
		t.Fatal("create project missing id")
	}
	return project.ID
}

func createSmokeSession(t *testing.T, baseURL, apiToken, projectID string) string {
	t.Helper()
	body := fmt.Sprintf(`{"project_id":%q,"posture":"build"}`, projectID)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create session request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create session status = %d body = %s", resp.StatusCode, respBody)
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session.ID == "" {
		t.Fatal("create session missing id")
	}
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return smokeSessionStatus(t, baseURL, apiToken, session.ID) == "idle"
	})
	return session.ID
}

func TestServeWithoutMockLLMPromptFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}

	moduleRoot := smokeModuleRoot
	bin := sharedServeBin

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	apiToken := "lycaon-smoke-no-mock-token"
	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	dbPath := filepath.Join(t.TempDir(), "no-mock-smoke.db")
	proc := exec.CommandContext(t.Context(), bin, "serve", "--db", dbPath)
	proc.Dir = moduleRoot
	proc.Env = append(os.Environ(),
		"HOME="+home,
		"LYCAON_LLM_MOCK=0",
		"LYCAON_ADDR="+addr,
		"LYCAON_API_TOKEN="+apiToken,
		"OPENAI_API_KEY=",
		"FIREWORKS_API_KEY=",
	)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	})

	baseURL := "http://" + addr
	waitForHealth(t, baseURL)

	sessionID := createSmokeSession(t, baseURL, apiToken, createSmokeProject(t, baseURL, apiToken, projectDir))

	operationID := uuid.NewString()
	promptReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/sessions/"+sessionID+"/prompts",
		strings.NewReader(fmt.Sprintf(`{"operation_id":%q,"text":"hello"}`, operationID)))
	if err != nil {
		t.Fatalf("prompt request: %v", err)
	}
	promptReq.Header.Set("Content-Type", "application/json")
	promptReq.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(promptReq)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", resp.StatusCode, body)
	}
	var accepted api.PromptAcceptedResponse
	if err := json.Unmarshal(body, &accepted); err != nil {
		t.Fatalf("decode prompt accept: %v", err)
	}
	assertSmokePromptIdentity(t, accepted, operationID)
	waitSmokePromptComplete(t, baseURL, apiToken, sessionID, accepted.MessageID, "hello", 30*time.Second)

	msgsReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/messages", nil)
	if err != nil {
		t.Fatalf("messages request: %v", err)
	}
	msgsReq.Header.Set("Authorization", "Bearer "+apiToken)
	msgsResp, err := http.DefaultClient.Do(msgsReq)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	defer msgsResp.Body.Close()
	var page api.SessionTranscriptPage
	if err := json.NewDecoder(msgsResp.Body).Decode(&page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	for _, msg := range page.Messages {
		if msg.Role == "assistant" && msg.Kind != "workflow_boundary" {
			t.Fatalf("expected no LLM assistant without provider, got %+v", msg)
		}
		if strings.Contains(msg.Content, "I understand. How can I help you further?") {
			t.Fatalf("response leaked mock catchphrase: %s", msg.Content)
		}
	}
}

func waitSmokePromptComplete(t *testing.T, baseURL, token, sessionID, messageID, text string, timeout time.Duration) {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pre := smokeSessionStatus(t, baseURL, token, sessionID)
		msgs := smokeListMessages(t, baseURL, token, sessionID)
		post := smokeSessionStatus(t, baseURL, token, sessionID)
		if pre == "idle" && post == "idle" && smokePromptStored(msgs, messageID, text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s prompt %q did not complete within %s", sessionID, text, timeout)
}

func smokeSessionStatus(t *testing.T, baseURL, token, sessionID string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatalf("session request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var sess struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return ""
	}
	return sess.Status
}

func smokeListMessages(t *testing.T, baseURL, token, sessionID string) []api.Message {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/messages", nil)
	if err != nil {
		t.Fatalf("messages request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	defer resp.Body.Close()
	var page api.SessionTranscriptPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	return page.Messages
}

func assertSmokePromptIdentity(t *testing.T, accepted api.PromptAcceptedResponse, operationID string) {
	t.Helper()
	if accepted.Status != "queued" && accepted.Status != "running" {
		t.Fatalf("prompt accept status = %q", accepted.Status)
	}
	if accepted.OperationID != operationID || accepted.MessageID == "" {
		t.Fatalf("prompt identity = %+v, want operation %s and admitted message", accepted, operationID)
	}
}

func smokePromptStored(msgs []api.Message, messageID, text string) bool {
	for _, msg := range msgs {
		if msg.ID == messageID && msg.Role == "user" && msg.Content == text {
			return true
		}
	}
	return false
}

func waitSmokeAssistantMessage(t *testing.T, baseURL, token, sessionID string, timeout time.Duration) string {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/messages", nil)
		if err != nil {
			t.Fatalf("messages request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var page api.SessionTranscriptPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		for _, msg := range page.Messages {
			if msg.Role == "assistant" && msg.Kind != "workflow_boundary" && strings.TrimSpace(msg.Content) != "" {
				return msg.ID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s did not produce assistant message within %s", sessionID, timeout)
	return ""
}
