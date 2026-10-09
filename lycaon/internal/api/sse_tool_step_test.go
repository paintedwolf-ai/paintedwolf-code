package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestToolStepReplayKeepsCardsWithoutProse(t *testing.T) {
	for _, cache := range []string{"absent", "content", "tokens"} {
		t.Run(cache, func(t *testing.T) {
			srv := newTestServer(t)
			sess := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
			msg := wire.Message{ID: "tool-step", Role: wire.MessageRoleAssistant, Content: "internal routing explanation",
				ToolCalls: []wire.ToolCall{{ID: "wait-call", Name: "wait", Args: map[string]any{"reason": "Waiting for the CLI implementation"}}}}
			testutil.FailErr(t, "append tool step", srv.sessionStore.AppendMessages(t.Context(), sess.ID, msg))
			if cache == "content" {
				srv.sessions.Transcript.Streams.CacheReplay(msg.ID, msg.Content, nil)
			}
			if cache == "tokens" {
				srv.sessions.Transcript.Streams.CacheReplay(msg.ID, msg.Content, []string{msg.Content})
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/stream?message="+msg.ID, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("replay status=%d body=%s", rec.Code, rec.Body.String())
			}
			var sawCard, sawReset, sawDone bool
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var chunk wire.PromptStreamChunk
				testutil.FailErr(t, "decode replay", json.Unmarshal([]byte(data), &chunk))
				if chunk.Token != "" {
					t.Fatalf("tool prose escaped via %s cache: %q", cache, chunk.Token)
				}
				sawCard = sawCard || len(chunk.ToolCalls) == 1 && chunk.ToolCalls[0].ID == "wait-call"
				sawReset = sawReset || chunk.Reset
				sawDone = sawDone || chunk.Done
			}
			if !sawCard || !sawReset || !sawDone {
				t.Fatalf("card=%v reset=%v done=%v", sawCard, sawReset, sawDone)
			}
			stored, err := srv.sessionStore.GetMessage(t.Context(), sess.ID, msg.ID)
			testutil.FailErr(t, "read recorded tool step", err)
			if stored.Content != msg.Content {
				t.Fatal("replay changed model history")
			}
		})
	}
}
