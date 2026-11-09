package api

import "testing"

func TestCompactionCheckpointDoesNotOpenAHumanTurn(t *testing.T) {
	user := Message{ID: "request", Role: MessageRoleUser, Origin: MessageOriginUser}
	checkpoint := Message{ID: "checkpoint", Role: MessageRoleUser, Origin: MessageOriginHost, CompactionCheckpoint: true}
	rows := []Message{user, checkpoint, {Role: MessageRoleAssistant, Content: "Recorded continuation"}}
	if IsUserIntentMessage(checkpoint) || UserIntentBoundary(rows) != 1 {
		t.Fatal("host checkpoint displaced the human turn")
	}
	latest, ok := LastUserIntentMessage(rows)
	if !ok || latest.ID != user.ID {
		t.Fatalf("latest request = %+v", latest)
	}
	if _, ok := LastUserIntentMessage(rows[1:]); ok {
		t.Fatal("host-only continuation acquired user intent")
	}
}
