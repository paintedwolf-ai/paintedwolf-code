package editordoc

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestAgentParticipantClocksRemainBoundedByChats(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	doc := f.open(t, "a.txt")
	for i := range 12 {
		edit := agentEdit(doc, fmt.Sprintf("revision %d\n", i))
		edit.SessionID = fmt.Sprintf("chat-%d", i%2)
		edit.ToolCallID = fmt.Sprintf("call-%d", i)
		result, err := f.service.ApplyAgentEdit(t.Context(), edit)
		testutil.FailErr(t, "apply chat operation", err)
		if !result.Saved || result.Document.Draft != edit.Content {
			t.Fatal("reused participant lost text")
		}
		doc = result.Document
	}
	var participants, contributions int
	testutil.FailErr(t, "count chat participants", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM editor_replicas WHERE document_id=? AND role='agent'`, doc.ID).Scan(&participants))
	testutil.FailErr(t, "count operation contributions", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_text_contributions WHERE document_id=? AND origin='agent'`, doc.ID).Scan(&contributions))
	if participants != 2 || contributions != 12 {
		t.Fatalf("participants=%d contributions=%d", participants, contributions)
	}
}

func TestPersonReplicaCannotClaimAnAgentIdentity(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	doc := f.open(t, "a.txt")
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(doc, "agent\n"))
	testutil.FailErr(t, "accept agent edit", err)
	var agentID uint32
	testutil.FailErr(t, "read agent participant", f.store.db.QueryRowContext(t.Context(), `SELECT replica_id FROM editor_replicas WHERE document_id=? AND role='agent'`, doc.ID).Scan(&agentID))
	joined, err := f.service.Join(t.Context(), doc.ID, doc.ProjectID, ReplicaJoin{ClientID: "chat-1", Incarnation: "chat-1", Epoch: result.Document.Epoch})
	testutil.FailErr(t, "join matching person name", err)
	if joined.ReplicaID == agentID {
		t.Fatal("person joined an agent participant")
	}
	_, err = f.service.SubmitReplica(t.Context(), doc.ProjectID, doc.ID, ReplicaSubmission{
		ClientID: "chat-1", ReplicaID: agentID, Epoch: joined.Epoch, OperationID: uuid.NewString(), Update: []byte{0},
	})
	if !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("agent identity submission error = %v", err)
	}
}
