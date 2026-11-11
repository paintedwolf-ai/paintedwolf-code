package api

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMidTurnNoteIDIsTheVersion5UUIDOfItsCarrier(t *testing.T) {
	for _, carrier := range []string{"", "m1", " 8a2f3c10-0000-4000-8000-000000000001 "} {
		want := uuid.NewSHA1(uuid.NameSpaceOID, []byte("agent-note:"+strings.TrimSpace(carrier))).String()
		if got := MidTurnNoteID(carrier); got != want {
			t.Fatalf("MidTurnNoteID(%q) = %s, want %s", carrier, got, want)
		}
	}
}
