package contract

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A workflow boundary row is a system-role message; the store must persist it
// and return it with its structured metadata.
func TestSchemaAllowsSystemMessageRole(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "boundary-row.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sessions := store.NewSQL(sqlDB)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create session", err)

	boundary := runstate.NewBoundaryMessage(&api.WorkflowRun{WorkflowID: "plan", WorkflowVersion: "1.0.1"}, "started", "research", "")
	contractcheck.FailErr(t, "append boundary row", sessions.AppendMessages(ctx, sess.ID, boundary))

	messages, err := sessions.GetMessages(ctx, sess.ID)
	contractcheck.FailErr(t, "read session messages", err)
	for _, msg := range messages {
		if msg.ID != boundary.ID {
			continue
		}
		if msg.Role != api.MessageRoleSystem || msg.Kind != api.MessageKindWorkflowBoundary {
			t.Fatalf("stored boundary role/kind = %q/%q", msg.Role, msg.Kind)
		}
		if msg.WorkflowBoundary == nil || !reflect.DeepEqual(*msg.WorkflowBoundary, *boundary.WorkflowBoundary) {
			t.Fatalf("stored boundary meta = %+v want %+v", msg.WorkflowBoundary, boundary.WorkflowBoundary)
		}
		return
	}
	t.Fatalf("boundary row %s not returned; messages = %+v", boundary.ID, messages)
}
