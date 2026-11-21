package httpio

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestProjectRegistryErrorNamesEveryLifecycleRefusal: a lifecycle refusal,
// however wrapped, answers its own code rather than an internal error.
func TestProjectRegistryErrorNamesEveryLifecycleRefusal(t *testing.T) {
	responder := &Responder{}
	for err, want := range map[error]wire.ApiErrorCode{
		project.ErrNotDraft:          wire.ApiErrorCodeProjectNotDraft,
		project.ErrPromotionNotFound: wire.ApiErrorCodePromotionNotFound,
		project.ErrPromotionConflict: wire.ApiErrorCodePromotionConflict,
		project.ErrNotFound:          wire.ApiErrorCodeProjectNotFound,
	} {
		w := httptest.NewRecorder()
		responder.ProjectRegistryError(w, httptest.NewRequest("POST", "/v1/projects/p/promotion", nil), fmt.Errorf("promote: %w", err))
		if got := decodeErrorBody(t, w); got.Code != want || w.Code != want.HTTPStatus() {
			t.Errorf("%v answered %d %s, want %d %s", err, w.Code, got.Code, want.HTTPStatus(), want)
		}
	}
}
