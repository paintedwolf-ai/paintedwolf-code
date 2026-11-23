package boards

import (
	"testing"

	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBoardResourcesFollowOnlyTheirOwnChatTree(t *testing.T) {
	db := testdbfixture.Open(t, "boards.db")
	for _, id := range []string{"root", "other"} {
		testdbseed.InsertSession(t, db, id, testdbseed.DefaultProjectID)
	}
	testdbseed.InsertChildSession(t, db, "worker", "root")
	testdbseed.InsertChildSession(t, db, "nested", "worker")
	for _, child := range []string{"worker", "nested"} {
		_, err := db.ExecContext(t.Context(), "UPDATE sessions SET agent_type = 'implementer' WHERE id = ?", child)
		testutil.FailErr(t, "attach worker", err)
	}
	runtime := New(Dependencies{Storage: persistence.Runtime{Sessions: store.NewSQL(db)}})
	for id, want := range map[string]string{"root": "root", "worker": "root", "nested": "root", "other": "other"} {
		if got := runtime.rootSessionKey(t.Context(), id); got != want {
			t.Errorf("resource root for %s = %s, want %s", id, got, want)
		}
	}
}
