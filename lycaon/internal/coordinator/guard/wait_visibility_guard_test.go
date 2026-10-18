package guard

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWaitVisibilityObservesCurrentBatch(t *testing.T) {
	wait := api.Message{Role: api.MessageRoleAssistant, Content: "Status update", ToolCalls: []api.ToolCall{{ID: "wait-1", Name: "wait"}}}
	silent := wait
	silent.Content = " \n"
	note := api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Visible status"}
	user := api.Message{Role: api.MessageRoleUser, Content: "Continue"}
	read := wait
	read.ToolCalls = []api.ToolCall{{ID: "read-1", Name: "read"}}
	for _, tc := range []struct {
		name                     string
		history                  []api.Message
		tool                     string
		offered, worker, blocked bool
	}{
		{"unpublished wait", []api.Message{wait}, "wait", true, false, true},
		{"silent wait", []api.Message{silent}, "wait", true, false, false},
		{"same batch note", []api.Message{wait, note}, "wait", true, false, false},
		{"previous batch note", []api.Message{note, wait}, "wait", true, false, true},
		{"no note tool", []api.Message{wait}, "wait", false, false, false},
		{"ordinary call", []api.Message{read}, "read", true, false, false},
		{"worker", []api.Message{wait}, "wait", true, true, false},
		{"no history", nil, "wait", true, false, false},
		{"new intent", []api.Message{wait, user}, "wait", true, false, false},
		{"older wait", []api.Message{wait, read}, "wait", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: "coordinator"}
			if tc.worker {
				sess.ParentSessionID = "parent"
			}
			gc := oar.NewGuardContext()
			ObserveWaitVisibility(sess, tc.history, tc.tool, tc.offered, gc)
			if got := hasRejectCode(gc, WaitHiddenTextCode); got != tc.blocked {
				t.Fatalf("blocked=%v, want %v", got, tc.blocked)
			}
		})
	}
}
