package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	internalapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webresearch"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// The canary satisfies the detector's shape and entropy requirements.
const outboundSecretCanary = "AKIAQYJK5TXV4NZR7SGB"

// Only container membership identifies this value; it matches no token pattern.
const outboundHarvestCanary = "b7Qk2wRt9YzE4pLm"

type recordedHTTPRequest struct {
	Method string
	URL    string
	Body   string
}

type recordingRoundTripper struct {
	mu   sync.Mutex
	reqs []recordedHTTPRequest
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		body = string(raw)
		req.Body = io.NopCloser(bytes.NewReader(raw))
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, recordedHTTPRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Body:   body,
	})
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"web":{"results":[]}}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (r *recordingRoundTripper) snapshot() []recordedHTTPRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedHTTPRequest, len(r.reqs))
	copy(out, r.reqs)
	return out
}

func installRecordingProviderHTTP(t *testing.T) *recordingRoundTripper {
	t.Helper()
	rec := &recordingRoundTripper{}
	webresearch.SetProviderHTTPClientForTest(&http.Client{Transport: rec})
	t.Cleanup(func() { webresearch.SetProviderHTTPClientForTest(nil) })
	return rec
}

func plantCanaryWorkspace(t *testing.T, dir string) {
	t.Helper()
	env := "AWS_ACCESS_KEY_ID=" + outboundSecretCanary + "\n" +
		"DB_PASSWORD=" + outboundHarvestCanary + "\n"
	testutil.FailErr(t, "write .env", os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600))
	sshDir := filepath.Join(dir, ".ssh")
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(sshDir, 0o700))
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nstub\n-----END OPENSSH PRIVATE KEY-----\n"
	testutil.FailErr(t, "write id_rsa", os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte(pem), 0o600))
}

func enableBraveSearchOnly(t *testing.T, srv *internalapi.Server) {
	t.Helper()
	putJSON(t, srv, http.MethodPut, "/v1/web-research/providers/brave/credential",
		`{"api_key":"brave-floor-test-key"}`)
	putJSON(t, srv, http.MethodPatch, "/v1/settings/web-research",
		`{"enabled_providers":["brave"]}`)
}

func putJSON(t *testing.T, srv *internalapi.Server, method, path, body string) {
	t.Helper()
	req := authedRequest(t, method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, path, w.Code, w.Body.String())
	}
}

func enableMCPProvider(t *testing.T, srv *internalapi.Server, id string) {
	t.Helper()
	enabled := true
	body, err := json.Marshal(wire.UpdateMcpProviderRequest{
		Enabled: &enabled,
	})
	testutil.FailErr(t, "marshal mcp enable", err)
	req := authedRequest(t, http.MethodPatch, "/v1/mcp/providers/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable mcp %s status=%d body=%s", id, w.Code, w.Body.String())
	}
}

func setGlobalApprovalPosture(t *testing.T, srv *internalapi.Server, posture string) {
	t.Helper()
	putJSON(t, srv, http.MethodPatch, "/v1/settings/approvals",
		fmt.Sprintf(`{"rules":[],"approval_posture":%q}`, posture))
}

func startPromptAsync(t *testing.T, h *wiring.Harness, sessionID, text string) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	t.Cleanup(func() {
		h.SessionMgr.Runner.Execution.Cancel(sessionID)
		cancel()
		select {
		case <-done:
		case <-time.After(testutil.Timeout(5 * time.Second)):
			t.Error("outbound prompt did not stop during cleanup")
		}
	})
	go func() {
		defer close(done)
		_, err := h.SessionMgr.Submissions.Prompt(ctx, sessionID, text)
		done <- err
	}()
	return done
}

func awaitOutboundPrompt(t *testing.T, h *wiring.Harness, sessionID string, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(testutil.Timeout(30 * time.Second)):
		err := fmt.Errorf("outbound prompt did not finish after its approval was resolved")
		dumpSessionFloorDebug(t, h, sessionID, err)
		h.SessionMgr.Runner.Execution.Cancel(sessionID)
		return err
	}
}

func waitOnePendingApproval(t *testing.T, h *wiring.Harness, sessionID string) wire.CheckpointEvent {
	t.Helper()
	ev, ok := tryWaitOnePendingApproval(t, h, sessionID, 8*time.Second)
	if !ok {
		t.Fatal("timed out waiting for tool_approval checkpoint")
	}
	return ev
}

func tryWaitOnePendingApproval(t *testing.T, h *wiring.Harness, sessionID string, d time.Duration) (wire.CheckpointEvent, bool) {
	t.Helper()
	var ev wire.CheckpointEvent
	ok := testutil.WaitForNoFatal(d, func() bool {
		pending, err := h.CheckpointMgr.ListPending(context.Background(), sessionID, nil)
		if err == nil && len(pending) == 1 && pending[0].Kind == wire.CheckpointKindToolApproval {
			ev = pending[0]
			return true
		}
		return false
	})
	return ev, ok
}

func dumpSessionFloorDebug(t *testing.T, h *wiring.Harness, sessionID string, promptErr error) {
	t.Helper()
	pending, _ := h.CheckpointMgr.ListPending(context.Background(), sessionID, nil)
	msgs, _ := h.Store.GetMessages(context.Background(), sessionID)
	t.Logf("promptErr=%v pending=%d", promptErr, len(pending))
	for _, p := range pending {
		t.Logf("pending kind=%s id=%s tp=%+v", p.Kind, p.ID, p.ToolApproval)
	}
	for _, m := range msgs {
		t.Logf("msg role=%s content=%q tools=%+v result=%+v", m.Role, truncateFloor(m.Content, 240), m.ToolCalls, m.ToolResult)
	}
}

func truncateFloor(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func resolveApproval(t *testing.T, srv *internalapi.Server, sessionID, checkpointID, action string) {
	t.Helper()
	body := fmt.Sprintf(`{"kind":"tool_approval","action":%q}`, action)
	if action == "approve" {
		body = `{"kind":"tool_approval","action":"approve","option_id":"send_unchanged"}`
	}
	resolveCheckpointHTTP(t, srv, sessionID, checkpointID, body)
}

func resolveCheckpointHTTP(t *testing.T, srv *internalapi.Server, sessionID, checkpointID, body string) {
	t.Helper()
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sessionID+"/checkpoints/"+checkpointID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve checkpoint status = %d body = %s", w.Code, w.Body.String())
	}
}

func assertToolMessageContains(t *testing.T, h *wiring.Harness, sessionID, want string) {
	t.Helper()
	msgs, err := h.Store.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages", err)
	for _, m := range msgs {
		if m.Role == wire.MessageRoleTool && strings.Contains(m.Content, want) {
			return
		}
	}
	t.Fatalf("expected tool message containing %q; got %+v", want, msgs)
}

func assertCanaryAbsent(t *testing.T, canary string, parts ...any) {
	t.Helper()
	for i, part := range parts {
		raw := stringifyCanaryPart(part)
		if strings.Contains(raw, canary) {
			t.Fatalf("canary leaked in part[%d]: %s", i, raw)
		}
	}
}

func stringifyCanaryPart(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case []recordedHTTPRequest:
		var b strings.Builder
		for _, r := range x {
			b.WriteString(r.Method)
			b.WriteByte(' ')
			b.WriteString(r.URL)
			b.WriteByte('\n')
			b.WriteString(r.Body)
			b.WriteByte('\n')
		}
		return b.String()
	case wire.CheckpointEvent:
		raw, _ := json.Marshal(x)
		return string(raw)
	case []wire.CheckpointEvent:
		raw, _ := json.Marshal(x)
		return string(raw)
	case map[string]any:
		raw, _ := json.Marshal(x)
		return string(raw)
	default:
		raw, _ := json.Marshal(x)
		if len(raw) == 0 {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}

func assertOutboundSecretAsk(t *testing.T, ev wire.CheckpointEvent) {
	t.Helper()
	if ev.ToolApproval == nil {
		t.Fatal("missing tool_approval payload")
	}
	plan := ev.ToolApproval.Plan
	if plan.Subject.Kind != "secret" || len(plan.Subject.Targets) != 1 {
		t.Fatalf("secret subject=%+v", plan.Subject)
	}
	assertCanaryAbsent(t, outboundSecretCanary, ev, plan.Subject, plan.Presentation)
}

// citesFact reports whether the card carried a machine fact under key with value.
// Presentation copy is written for a reader; the wire values stay here.
func citesFact(cited []wire.PresentedFact, key, value string) bool {
	for _, fact := range cited {
		if fact.Key == key && fact.Value == value {
			return true
		}
	}
	return false
}
