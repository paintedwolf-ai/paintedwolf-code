package closeouts

import (
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSynthesisDelayManager(t *testing.T) (*Service, *api.Session) {
	t.Helper()
	data := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := data.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create closeout fixture", err)
	return New(data), sess
}
