package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSessionDraftVersionsOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	sess := createSessionHTTP(t, h.Server, t.TempDir())
	ctx := t.Context()
	slotID := uuid.NewString()
	testutil.FailErr(t, "append draft slot message", h.Store.AppendMessages(ctx, sess.ID, wire.Message{
		ID:      slotID,
		Role:    wire.MessageRoleAssistant,
		Content: "second draft",
	}))
	_, err := h.Store.AppendDraftVersion(ctx, sess.ID, slotID, "first draft", "")
	testutil.FailErr(t, "append draft version", err)

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/drafts/"+slotID+"/versions", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}/drafts/{slot_id}/versions", map[string]string{
		"id":      sess.ID,
		"slot_id": slotID,
	})
	var body wire.DraftVersionsResponse
	testutil.FailErr(t, "decode draft versions", json.Unmarshal(w.Body.Bytes(), &body))
	if len(body.Versions) != 1 || body.Versions[0].Body != "first draft" {
		t.Fatalf("versions = %+v", body.Versions)
	}
}
