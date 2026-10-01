package store

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLSessionUpdatesCarryCurrentTurn(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	st.SetEventOutbox(eventoutbox.New(database, events.NewMemoryHub()))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for turn := 1; turn <= 2; turn++ {
		testutil.FailErr(t, "append user intent", st.AppendMessages(t.Context(), sess.ID, api.Message{
			ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "edit the file",
		}))
		testutil.FailErr(t, "update busy session", st.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusBusy))
		var data []byte
		testutil.FailErr(t, "read durable session event", database.QueryRowContext(t.Context(),
			`SELECT data_json FROM event_outbox WHERE topic = ? AND session_id = ? ORDER BY id DESC LIMIT 1`,
			string(api.EventTopicSession), sess.ID).Scan(&data))
		var event api.SessionEvent
		testutil.FailErr(t, "decode session event", json.Unmarshal(data, &event))
		if event.CurrentTurn != turn {
			t.Fatalf("session event current turn = %d, want %d", event.CurrentTurn, turn)
		}
	}
}
