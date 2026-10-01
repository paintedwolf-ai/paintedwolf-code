package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type lifecycleLogBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *lifecycleLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(data)
}
func (b *lifecycleLogBuffer) text() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

func TestRequestLifecycleScreensBeforeTransportAndRecordsSafeTimings(t *testing.T) {
	var logs lifecycleLogBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if bytes.Contains(body, []byte(modelScreenGitHubToken)) {
			t.Error("unscreened secret reached transport")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(server.Close)
	decision := secretmatch.Withhold
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: decision}, nil
	})
	driver := openaicompat.New("local", server.URL, "", []modelinfo.Entry{{ID: "model"}})
	provider := &lifecycleProvider{Provider: &secretScreenedProvider{
		inner: &dispatchProvider{Provider: driver}, screen: screen, destination: ScreenDestination{ID: "local"},
	}}
	dispatched := 0
	ctx := WithDispatchObserver(t.Context(), func() { dispatched++ })
	req := modelSecretRequest()
	req.Model = "model"
	req.Debug.CallID = "first"
	if _, err := provider.Complete(ctx, req); !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("screen refusal = %v", err)
	}
	if requests.Load() != 0 || dispatched != 0 {
		t.Fatal("withheld request reached transport")
	}
	decision = secretmatch.SendRedacted
	req.Debug.CallID = "second"
	_, err := provider.Complete(ctx, req)
	testutil.FailErr(t, "screened completion", err)
	if requests.Load() != 1 || dispatched != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
	text := logs.text()
	if strings.Contains(text, modelScreenGitHubToken) {
		t.Fatal("timing log retained secret")
	}
	phases := map[string][]string{}
	for _, line := range bytes.Split([]byte(text), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row struct {
			CallID string `json:"call_id"`
			Phase  string `json:"phase"`
		}
		testutil.FailErr(t, "decode timing log", json.Unmarshal(line, &row))
		if row.Phase != "" {
			phases[row.CallID] = append(phases[row.CallID], row.Phase)
		}
	}
	want := []string{"preparing", "screened", "dispatch", "connection_wait", "connected", "request_written", "response_started", "finished"}
	got := phases["second"]
	if len(got) != len(want) || !slices.Equal(got[:3], want[:3]) || got[len(got)-1] != "finished" {
		t.Fatalf("request phases = %v", got)
	}
	for _, phase := range want {
		if !slices.Contains(got, phase) {
			t.Fatalf("missing phase %s", phase)
		}
	}
	if !slices.Equal(phases["first"], []string{"preparing", "screened", "finished"}) {
		t.Fatalf("refused phases = %v", phases["first"])
	}
}
