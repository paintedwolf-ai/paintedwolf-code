package sessionadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDiscardingBlueprintChatPreservesSourceTranscript(t *testing.T) {
	server, _, source := contractfixture.NewCheckpointHandlerFixture(t)
	lifecycle := server.Admin.SessionAdmin.Lifecycle
	chat, err := lifecycle.MaterializeSession(t.Context(), wire.CreateSessionRequest{ProjectID: source.ProjectID, Posture: wire.SessionPostureBuild})
	testutil.FailErr(t, "materialize blueprint chat", err)
	if chat.ID == source.ID || chat.ProjectID != source.ProjectID {
		t.Fatalf("materialized chat = %+v", chat)
	}
	row := wire.Message{ID: "source-draft", Role: wire.MessageRoleAssistant, Content: "Source draft"}
	testutil.FailErr(t, "append source draft", lifecycle.Sessions.Runner.Transcript.AppendPlain(t.Context(), source.ID, row))
	read := func(id, suffix string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+id+suffix, nil))
		return w
	}
	versions := "/drafts/" + row.ID + "/versions"
	checkCode := func(resp *httptest.ResponseRecorder, want wire.ApiErrorCode) {
		t.Helper()
		var refused wire.ErrorResponse
		testutil.FailErr(t, "decode refusal", json.Unmarshal(resp.Body.Bytes(), &refused))
		if resp.Code != http.StatusNotFound || refused.Code != want {
			t.Fatalf("refusal = %d %s", resp.Code, resp.Body.String())
		}
	}
	checkCode(read(chat.ID, versions), wire.ApiErrorCodeMessageNotFound)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	lifecycle.DiscardMaterializedSession(canceled, chat.ID)
	checkCode(read(chat.ID, versions), wire.ApiErrorCodeSessionNotFound)
	resp := read(source.ID, versions)
	var history wire.DraftVersionsResponse
	testutil.FailErr(t, "read original draft history", json.Unmarshal(resp.Body.Bytes(), &history))
	if resp.Code != http.StatusOK || history.Versions == nil || len(history.Versions) != 0 {
		t.Fatalf("original history = %d %s", resp.Code, resp.Body.String())
	}
	resp = read(source.ID, "/bootstrap")
	var boot wire.SessionBootstrap
	testutil.FailErr(t, "read source bootstrap", json.Unmarshal(resp.Body.Bytes(), &boot))
	if resp.Code != http.StatusOK || boot.Session.ID != source.ID || len(boot.Transcript.Messages) != 1 || boot.Transcript.Messages[0].ID != row.ID {
		t.Fatalf("source bootstrap = %d %s", resp.Code, resp.Body.String())
	}
}
