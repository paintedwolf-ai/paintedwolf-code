package smoke_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	child := startSmokeProcess(t, proc)

	baseURL := "http://" + addr
	child.waitForHealth(t, baseURL)

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
	waitSmokePromptComplete(t, baseURL, apiToken, sessionID, accepted.MessageID, "hello")

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
