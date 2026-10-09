package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestStreamReplayRequiresTranscriptMembership(t *testing.T) {
	for _, mode := range []string{"tokens", "content", "persisted", "active"} {
		t.Run(mode, func(t *testing.T) {
			srv := newTestServer(t)
			a := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
			b := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
			msg := wire.Message{ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Content: "session A content"}
			testutil.FailErr(t, "append message", srv.sessionStore.AppendMessages(t.Context(), a.ID, msg))
			switch mode {
			case "tokens":
				srv.sessions.Transcript.Streams.CacheReplay(msg.ID, msg.Content, []string{msg.Content})
			case "content":
				srv.sessions.Transcript.Streams.CacheReplay(msg.ID, msg.Content, nil)
			case "active":
				srv.sessions.Transcript.Streams.CacheLive(a.ID, msg.ID, msg.Content, nil, 1)
			}
			request := func(sessionID string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, newAuthedRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/stream?message="+msg.ID, nil))
				return w
			}
			assertErrorResponse(t, request(b.ID), http.StatusNotFound, "message_not_found")
			if mode == "active" {
				srv.sessions.Transcript.Streams.Finish(t.Context(), a.ID)
			}
			if w := request(a.ID); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), msg.Content) {
				t.Fatalf("owner replay status=%d body=%s", w.Code, w.Body.String())
			}
			_, err := srv.sessionStore.TruncateMessagesFrom(t.Context(), a.ID, msg.ID)
			testutil.FailErr(t, "truncate transcript", err)
			assertErrorResponse(t, request(a.ID), http.StatusNotFound, "message_not_found")
			testutil.FailErr(t, "delete session", srv.sessionStore.Delete(t.Context(), a.ID))
			if w := request(a.ID); w.Code != http.StatusNotFound {
				t.Fatalf("deleted session status=%d", w.Code)
			}
		})
	}
}

func TestSettledLiveSubscriptionRechecksTranscript(t *testing.T) {
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
	messageID := uuid.NewString()
	srv.sessions.Transcript.Streams.CacheReplay(messageID, "removed content", nil)
	w := httptest.NewRecorder()
	r := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/stream?message="+messageID, nil)
	srv.followLiveStream(r, w, w, sess.ID, messageID)
	assertErrorResponse(t, w, http.StatusNotFound, "message_not_found")
}
