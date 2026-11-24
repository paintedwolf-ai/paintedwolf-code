package gitadmin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCheckoutAdmittedAndResolveValidation(t *testing.T) {
	if CheckoutAdmitted("") || CheckoutAdmitted("   ") {
		t.Fatal("expected blank checkout path to be refused")
	}
	var nilHandler *Handler
	_, _, code, _, ok := nilHandler.ResolveCheckoutProject(context.Background(), nil, "not-a-uuid")
	if ok || code != wire.ApiErrorCodeInvalidRequest {
		t.Fatalf("expected invalid_request for malformed UUID, got ok=%v code=%q", ok, code)
	}
	_, _, code, _, ok = nilHandler.ResolveCheckoutProject(context.Background(), nil, "00000000-0000-4000-8000-000000000001")
	if ok || code != wire.ApiErrorCodeWorktreeNotFound {
		t.Fatalf("expected worktree_not_found on nil handler, got ok=%v code=%q", ok, code)
	}
}

func TestWriteCheckoutMutationErrorMappings(t *testing.T) {
	h := &Handler{responses: httpio.NewResponses(nil)}
	cases := []struct {
		err        error
		wantStatus int
	}{
		{err: worktrees.ErrCheckoutNotFound, wantStatus: http.StatusNotFound},
		{err: worktrees.ErrCheckoutRetired, wantStatus: http.StatusConflict},
		{err: worktrees.ErrCheckoutUnavailable, wantStatus: http.StatusConflict},
		{err: git.ErrExpectedHeadMismatch, wantStatus: http.StatusConflict},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/projects/p/git/repos/r/commit", nil)
		writeCheckoutMutationError(h, w, req, wire.ApiErrorCodeGitCommitFailed, "commit failed", tc.err)
		if w.Code != tc.wantStatus {
			t.Fatalf("err=%v status=%d want=%d body=%s", tc.err, w.Code, tc.wantStatus, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/projects/p/git/repos/r/commit", nil)
	writeCheckoutMutationError(h, w, req, wire.ApiErrorCodeGitCommitFailed, "commit failed", errors.New("boom"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fallback status=%d want=422 body=%s", w.Code, w.Body.String())
	}
}
