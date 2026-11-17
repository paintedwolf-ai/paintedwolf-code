package hostcontracts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestStreamReplayRequiresTranscriptMembership(t *testing.T) {
	for _, mode := range []string{"tokens", "content", "persisted", "active"} {
		t.Run(mode, func(t *testing.T) {
			srv := contractfixture.NewTestServer(t)
			a := contractfixture.CreateSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
			b := contractfixture.CreateSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
			msg := wire.Message{ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Content: "session A content"}
			testutil.FailErr(t, "append message", srv.Sources.Workspace.SessionStore.AppendMessages(t.Context(), a.ID, msg))
			switch mode {
			case "tokens":
				srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Transcript.Streams.CacheReplay(msg.ID, msg.Content, []string{msg.Content})
			case "content":
				srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Transcript.Streams.CacheReplay(msg.ID, msg.Content, nil)
			case "active":
				srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Transcript.Streams.CacheLive(a.ID, msg.ID, msg.Content, nil, 1)
			}
			request := func(sessionID string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/stream?message="+msg.ID, nil))
				return w
			}
			contractfixture.AssertErrorResponse(t, request(b.ID), http.StatusNotFound, "message_not_found")
			if mode == "active" {
				srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Transcript.Streams.Finish(t.Context(), a.ID)
			}
			if w := request(a.ID); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), msg.Content) {
				t.Fatalf("owner replay status=%d body=%s", w.Code, w.Body.String())
			}
			rewindFrom(t, srv.Sources.Workspace.SessionStore, a.ID, msg.ID)
			contractfixture.AssertErrorResponse(t, request(a.ID), http.StatusNotFound, "message_not_found")
			testutil.FailErr(t, "delete session", srv.Sources.Workspace.SessionStore.Delete(t.Context(), a.ID))
			if w := request(a.ID); w.Code != http.StatusNotFound {
				t.Fatalf("deleted session status=%d", w.Code)
			}
		})
	}
}

// rewindFrom truncates a session from anchor through the committed rewind path.
func rewindFrom(t *testing.T, st interface {
	PrepareRewind(context.Context, store.RewindOperation) error
	SetRewindPhase(context.Context, string, string, string) error
	CommitRewind(context.Context, string, string, string, wire.RewindSessionResponse) (int, error)
}, sessionID, anchor string) {
	t.Helper()
	id := uuid.NewString()
	testutil.FailErr(t, "prepare rewind", st.PrepareRewind(t.Context(), store.RewindOperation{ID: id, SessionID: sessionID, AnchorMessageID: anchor, InputDigest: "test"}))
	testutil.FailErr(t, "apply rewind files", st.SetRewindPhase(t.Context(), id, "files_applied", ""))
	_, err := st.CommitRewind(t.Context(), id, sessionID, anchor, wire.RewindSessionResponse{})
	testutil.FailErr(t, "commit rewind", err)
}
