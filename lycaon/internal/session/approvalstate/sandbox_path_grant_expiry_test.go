package approvalstate_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

// A day-bounded path grant stops carrying authority at its deadline while its
// row stays listed for Saved approvals, and it can be reinstalled once it has
// lapsed; a chat-lifetime grant keeps its authority.
func TestSandboxPathGrantExpiresAtItsDeadline(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	rootSession := "coord-sess"
	dayRoot, chatRoot := filepath.Join(t.TempDir(), "day"), filepath.Join(t.TempDir(), "chat")
	past := time.Now().UTC().Add(-time.Minute)
	future := time.Now().UTC().Add(time.Hour)
	if !rt.GrantChat(rootSession, dayRoot, "grant_day", "cp-day", &past) {
		t.Fatal("expired grant was not stored")
	}
	if !rt.GrantChat(rootSession, chatRoot, "grant_chat", "cp-chat", nil) {
		t.Fatal("chat grant was not stored")
	}
	if roots := rt.SessionWriteRoots(rootSession); len(roots) != 1 || roots[0] != filepath.Clean(chatRoot) {
		t.Fatalf("roots past the deadline = %v, want only the chat root", roots)
	}
	if grants := rt.ListChatGrants(rootSession); len(grants) != 2 {
		t.Fatalf("listed grants = %+v, want the expired row kept for Saved approvals", grants)
	}
	if found, _, ok := rt.FindByID("grant_day"); !ok || found.Live(time.Now()) {
		t.Fatalf("expired grant = %+v found=%v, want listed and not live", found, ok)
	}
	if !rt.GrantChat(rootSession, dayRoot, "grant_day", "cp-day-2", &future) {
		t.Fatal("a lapsed grant must be replaceable")
	}
	found, _, ok := rt.FindByID("grant_day")
	if !ok || found.SourceCheckpointID != "cp-day-2" || !found.Live(time.Now()) {
		t.Fatalf("renewed grant = %+v found=%v", found, ok)
	}
	if roots := rt.SessionWriteRoots(rootSession); len(roots) != 2 {
		t.Fatalf("roots after renewal = %v, want both", roots)
	}
	if rt.GrantChat(rootSession, dayRoot, "grant_day", "cp-day-3", &future) {
		t.Fatal("a live grant must not be replaced")
	}
}
