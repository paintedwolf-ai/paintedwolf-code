package policyfacts

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// Every occurrence names its recipient: a worker leg's session_role is
// worker, as the block plane and worker report producers publish it.
func TestSessionFactsNameTheRecipientRole(t *testing.T) {
	manager := New(nil)
	for _, tc := range []struct {
		sess       *api.Session
		role       string
		workerLeg  bool
		withRecord string
	}{
		{&api.Session{ID: "coord"}, "coordinator", false, "coordinator session"},
		{&api.Session{ID: "leg", ParentSessionID: "coord"}, "worker", true, "worker leg"},
	} {
		gc := oar.NewGuardContext()
		manager.FillSessionFacts(t.Context(), gc, tc.sess, "read", nil)
		if gc.Session.Role != tc.role || gc.Session.WorkerLeg != tc.workerLeg {
			t.Fatalf("%s: role=%q worker_leg=%v, want %q and %v", tc.withRecord, gc.Session.Role, gc.Session.WorkerLeg, tc.role, tc.workerLeg)
		}
	}
}
