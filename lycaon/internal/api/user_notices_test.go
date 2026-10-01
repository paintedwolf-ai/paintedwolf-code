package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWriteErrorRendersUserNotice(t *testing.T) {
	cfg, err := usernotice.LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	if err != nil {
		t.Fatalf("load user notices: %v", err)
	}
	s := &Server{responses: httpio.Responder{Notices: usernotice.NewCatalog(cfg)}}
	rec := httptest.NewRecorder()
	s.responses.Fail(rec, wire.ApiErrorCodeProviderNotConfigured, "debug detail")

	if rec.Code != wire.ApiErrorCodeProviderNotConfigured.HTTPStatus() {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No model configured") || !strings.Contains(body, "language model") {
		t.Fatalf("body = %s", body)
	}
	if strings.Contains(body, "debug detail") {
		t.Fatal("debug message leaked to wire")
	}
}

func TestActionFailuresKeepUsefulSafeNotices(t *testing.T) {
	s := &Server{responses: httpio.Responder{Notices: testUserNotices(t)}}
	for _, tc := range []struct {
		code   wire.ApiErrorCode
		status int
		action string
	}{
		{"clone_failed", http.StatusBadGateway, "repository URL"},
		{"folder_exists", http.StatusConflict, "another folder name"},
		{"folder_not_empty", http.StatusConflict, "empty folder"},
		{"invalid_path", http.StatusBadRequest, "existing folder"},
		{"backup_restore_pending", http.StatusConflict, "Restart the app"},
		{"duplicate_root_label", http.StatusConflict, "different folder label"},
		{"duplicate_root", http.StatusConflict, "existing folder"},
		{"root_busy", http.StatusConflict, "active work"},
		{"choice_transition_pending_input", http.StatusConflict, "answer the pending question"},
		{"editor_revision_conflict", http.StatusConflict, "Retry the action against the current file"},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			response := httptest.NewRecorder()
			s.responses.Fail(response, tc.code, "transport debug: https://disposable-token@example.test/private")
			var body wire.ErrorResponse
			testutil.FailErr(t, "decode failure", json.Unmarshal(response.Body.Bytes(), &body))
			if response.Code != tc.status || body.Code != tc.code || !strings.Contains(body.SuggestedAction, tc.action) {
				t.Fatalf("failure lost its recovery action: status %d, %+v", response.Code, body)
			}
			if strings.Contains(response.Body.String(), "disposable-token") || strings.Contains(body.Message, "no more detail") {
				t.Fatalf("failure leaked diagnostics or used fallback: %+v", body)
			}
		})
	}
}
