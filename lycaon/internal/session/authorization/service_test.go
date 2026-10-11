package authorization

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestConfiguredSealFailsClosedWhenSealerUnavailable(t *testing.T) {
	service := New()
	service.authzSealRequired = true
	err := service.Seal(t.Context(), &api.Session{ID: "session"}, "coordinator", "")
	if !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("missing authorization seal error=%v", err)
	}
	if service.Wired() {
		t.Fatal("missing sealer reported as wired")
	}
}
