package sessioncontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExportSessionTranscriptFormats(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)
	created := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	patchBody := `{"title":"Ship readiness"}`
	patchReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/sessions/"+created.ID, strings.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	srv.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}

	testutil.FailErr(t, "append user", srv.Sources.Workspace.SessionStore.AppendMessages(t.Context(), created.ID, wire.Message{
		Role:    wire.MessageRoleUser,
		Content: "please ship",
	}))
	testutil.FailErr(t, "append chrome", srv.Sources.Workspace.SessionStore.AppendMessages(t.Context(), created.ID, wire.Message{
		Role:           wire.MessageRoleSystem,
		Kind:           wire.MessageKindProgressUpdate,
		Content:        "should not appear in md",
		WorkflowRunID:  "run-export-chrome",
		ProgressUpdate: &wire.ProgressUpdateMeta{},
		Visibility:     wire.MessageVisibilityTranscript,
	}))
	testutil.FailErr(t, "append assistant", srv.Sources.Workspace.SessionStore.AppendMessages(t.Context(), created.ID, wire.Message{
		Role:    wire.MessageRoleAssistant,
		Content: "working on it",
	}))

	mdReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/export?format=md", nil)
	mdRec := httptest.NewRecorder()
	srv.ServeHTTP(mdRec, mdReq)
	if mdRec.Code != http.StatusOK {
		t.Fatalf("md status=%d body=%s", mdRec.Code, mdRec.Body.String())
	}
	if ct := mdRec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("md content-type=%q", ct)
	}
	cd := mdRec.Header().Get("Content-Disposition")
	short := created.ID
	if len(short) > 8 {
		short = short[:8]
	}
	wantName := `filename="Ship-readiness-` + short + `.md"`
	if !strings.Contains(cd, wantName) {
		t.Fatalf("content-disposition=%q want substring %q", cd, wantName)
	}
	body := mdRec.Body.String()
	if !strings.Contains(body, "## You") || !strings.Contains(body, "please ship") {
		t.Fatalf("md missing user turn: %s", body)
	}
	if strings.Contains(body, "should not appear in md") {
		t.Fatal("md included chrome")
	}

	jsonReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/export?format=json", nil)
	jsonRec := httptest.NewRecorder()
	srv.ServeHTTP(jsonRec, jsonReq)
	if jsonRec.Code != http.StatusOK {
		t.Fatalf("json status=%d body=%s", jsonRec.Code, jsonRec.Body.String())
	}
	if ct := jsonRec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("json content-type=%q", ct)
	}
	var page wire.SessionTranscriptPage
	testutil.FailErr(t, "decode json export", json.Unmarshal(jsonRec.Body.Bytes(), &page))
	if len(page.Messages) < 3 {
		t.Fatalf("json page messages=%d", len(page.Messages))
	}
	foundChrome := false
	for _, m := range page.Messages {
		if m.Kind == wire.MessageKindProgressUpdate {
			foundChrome = true
		}
	}
	if !foundChrome {
		t.Fatal("json export must keep full-fidelity chrome rows")
	}

	badReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/export?format=pdf", nil)
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad format status=%d", badRec.Code)
	}
	var errBody wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.Unmarshal(badRec.Body.Bytes(), &errBody))
	if errBody.Code != "export_format_invalid" {
		t.Fatalf("code=%q", errBody.Code)
	}

	missingReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/00000000-0000-0000-0000-000000000000/export", nil)
	missingRec := httptest.NewRecorder()
	srv.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d", missingRec.Code)
	}
}
