package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// sessionRequestReader projects sess's next request through the host; an
// unprojectable session yields the invalid Reader production would pass.
func sessionRequestReader(t *testing.T, mgr *Host, sess *api.Session) prompts.Reader {
	t.Helper()
	reader, err := mgr.Coordinator.Context.NextRequestReader(t.Context(), sess, nil)
	if err != nil {
		t.Logf("request reader for %s: %v", sess.ID, err)
	}
	return reader
}
