package closeouts

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSynthesisDelayManager(t *testing.T) (*Service, *api.Session) {
	t.Helper()
	data := store.NewMemory()
	sess, err := data.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create closeout fixture", err)
	return New(data), sess
}
