package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type cancelingStreamWriter struct {
	header  http.Header
	body    strings.Builder
	cancel  context.CancelFunc
	flushes int
}

func (w *cancelingStreamWriter) Header() http.Header { return w.header }
func (w *cancelingStreamWriter) WriteHeader(int)     {}
func (w *cancelingStreamWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}
func (w *cancelingStreamWriter) Flush() {
	w.flushes++
	if w.flushes == 1 {
		w.cancel()
	}
}

func TestStreamReplaysPersistedContentExactly(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	content := "hello  world\n\n```go\nfmt.Println(\"x\")\n```"
	msg := wire.Message{ID: "msg-evicted", Role: wire.MessageRoleAssistant, Content: content}
	testutil.FailErr(t, "append persisted message", srv.sessionStore.AppendMessages(t.Context(), sess.ID, msg))

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/stream?message="+msg.ID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var replay strings.Builder
	done := false
	reset := false
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk wire.PromptStreamChunk
		testutil.FailErr(t, "decode replay chunk", json.Unmarshal([]byte(data), &chunk))
		replay.WriteString(chunk.Token)
		reset = reset || chunk.Reset
		done = done || chunk.Done
	}
	if replay.String() != content {
		t.Fatalf("replay = %q, want exact %q", replay.String(), content)
	}
	if !done {
		t.Fatal("replay missing done chunk")
	}
	if !reset {
		t.Fatal("persisted snapshot replay must reset stale client content")
	}
}

func TestStreamUnknownMessageStillNotFoundWithFallback(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	req := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/stream?message=msg-absent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReplayStopsImmediatelyWhenRequestIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	w := &cancelingStreamWriter{header: make(http.Header), cancel: cancel}
	if replayPromptChunks(ctx, nil, w, w, []string{"first", "second"}, time.Hour) {
		t.Fatal("replay reported completion after cancellation")
	}
	if strings.Contains(w.body.String(), "second") {
		t.Fatalf("replay wrote after cancellation: %q", w.body.String())
	}
}

func TestReplayResetsFirstDeltaThenAppends(t *testing.T) {
	rec := httptest.NewRecorder()
	if !replayPromptChunks(t.Context(), nil, rec, rec, []string{"hel", "lo"}, 0) {
		t.Fatal("replay did not complete")
	}
	var chunks []wire.PromptStreamChunk
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk wire.PromptStreamChunk
		testutil.FailErr(t, "decode replay chunk", json.Unmarshal([]byte(data), &chunk))
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 2 || !chunks[0].Reset || chunks[1].Reset {
		t.Fatalf("replay reset sequence = %+v", chunks)
	}
}
