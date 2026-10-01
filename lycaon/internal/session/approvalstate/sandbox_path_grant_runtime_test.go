package approvalstate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

func TestSandboxPathGrantRuntimeDenySet(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	proposed := "/opt/cache"

	action, id := rt.Begin(root, proposed, "tc-1")
	if action != approvalstate.SandboxAskMint || id != "" {
		t.Fatalf("Begin mint: action=%v id=%q", action, id)
	}
	rt.RegisterPending(root, proposed, "cp-1")
	rt.Finish(root, proposed, true) // deny

	action, _ = rt.Begin(root, proposed, "tc-2")
	if action != approvalstate.SandboxAskSkipDenied {
		t.Fatalf("after deny want SkipDenied, got %v", action)
	}
	rt.ClearDenied(root, proposed)
	action, _ = rt.Begin(root, proposed, "tc-3")
	if action != approvalstate.SandboxAskMint {
		t.Fatalf("after ClearDenied want Mint, got %v", action)
	}
}

func TestSandboxPathGrantRuntimePendingCoalesce(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	proposed := "/opt/cache"

	action, _ := rt.Begin(root, proposed, "tc-a")
	if action != approvalstate.SandboxAskMint {
		t.Fatalf("first Begin: %v", action)
	}
	rt.RegisterPending(root, proposed, "cp-shared")

	action, id := rt.Begin(root, proposed, "tc-b")
	if action != approvalstate.SandboxAskJoin || id != "cp-shared" {
		t.Fatalf("second Begin: action=%v id=%q", action, id)
	}
}

func TestSandboxPathGrantRuntimeUserTurnClearsDeny(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	chat := "chat-1"
	proposed := "/opt/cache"

	_, _ = rt.Begin(chat, proposed, "tc-1")
	rt.RegisterPending(chat, proposed, "cp-open")
	rt.Finish(chat, proposed, true)

	action, _ := rt.Begin(chat, proposed, "tc-2")
	if action != approvalstate.SandboxAskSkipDenied {
		t.Fatalf("after deny want SkipDenied, got %v", action)
	}

	rt.NoteUserIntentBoundary(chat)

	action, _ = rt.Begin(chat, proposed, "tc-3")
	if action != approvalstate.SandboxAskMint {
		t.Fatalf("user turn must clear deny-set, got %v", action)
	}
}

func TestSandboxPathGrantRuntimePerToolCall(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"

	action, _ := rt.Begin(root, "/opt/a", "same-tc")
	if action != approvalstate.SandboxAskMint {
		t.Fatalf("first: %v", action)
	}
	// One invocation enters the guard once.
	action, _ = rt.Begin(root, "/opt/b", "same-tc")
	if action != approvalstate.SandboxAskSkipToolCall {
		t.Fatalf("same tool_call want SkipToolCall, got %v", action)
	}
}

func TestSandboxWriteRootSessionOverlayKeepsMissingPath(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	live := t.TempDir()
	missing := filepath.Join(t.TempDir(), "not-yet-created")

	rt.GrantSessionWriteRoot(root, live)
	rt.GrantSessionWriteRoot(root, missing)
	got := rt.SessionWriteRoots(root)
	if len(got) != 2 {
		t.Fatalf("want both overlay roots, including a path that does not exist yet, got %v", got)
	}
	foundMissing := false
	for _, gotRoot := range got {
		if gotRoot == filepath.Clean(missing) {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("missing path dropped from overlay: %v", got)
	}
}

func TestSandboxWriteRootSessionOverlayCapFIFO(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	base := t.TempDir()
	dirs := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		d := filepath.Join(base, fmt.Sprintf("r%02d", i))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		dirs = append(dirs, d)
		rt.GrantSessionWriteRoot(root, d)
	}
	got := rt.SessionWriteRoots(root)
	if len(got) != 32 {
		t.Fatalf("overlay must cap at 32, got %d", len(got))
	}
	// FIFO: the 8 oldest are evicted; the newest survive.
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	if set[filepath.Clean(dirs[0])] {
		t.Fatal("oldest entry should be evicted first (FIFO)")
	}
	if !set[filepath.Clean(dirs[39])] {
		t.Fatal("newest entry must survive")
	}
}

func TestSandboxWriteRootSessionOverlayRegrantPreservesFIFO(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	a, b := t.TempDir(), t.TempDir()
	rt.GrantSessionWriteRoot(root, a)
	rt.GrantSessionWriteRoot(root, b)
	rt.GrantSessionWriteRoot(root, a)
	got := rt.SessionWriteRoots(root)
	if len(got) != 2 {
		t.Fatalf("re-grant must not duplicate, got %v", got)
	}
	if got[0] != filepath.Clean(a) {
		t.Fatalf("re-granted root moved in FIFO: %v", got)
	}
}

func TestSandboxWriteRootTaskGrantInventoryAndRevoke(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	rootSession, writeRoot := "coord-sess", t.TempDir()
	rt.GrantChat(rootSession, writeRoot, "grant_write_root", "checkpoint-1", nil)

	grants := rt.ListChatGrants(rootSession)
	if len(grants) != 1 || grants[0].ID != "grant_write_root" || grants[0].Root != filepath.Clean(writeRoot) {
		t.Fatalf("task grants = %+v", grants)
	}
	if found, sessionID, ok := rt.FindByID("grant_write_root"); !ok || sessionID != rootSession || found.Root != filepath.Clean(writeRoot) {
		t.Fatalf("FindByID = %+v, %q, %t", found, sessionID, ok)
	}
	if _, ok := rt.RevokeByID("grant_write_root"); !ok {
		t.Fatal("RevokeByID did not find task grant")
	}
	if roots := rt.SessionWriteRoots(rootSession); len(roots) != 0 {
		t.Fatalf("roots after revoke = %v", roots)
	}
}

func TestSandboxWriteRootTaskGrantKeepsSeparateInstallers(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	rootSession, writeRoot := "coord-sess", t.TempDir()
	if !rt.GrantChat(rootSession, writeRoot, "grant_a", "checkpoint-1", nil) {
		t.Fatal("initial grant was not stored")
	}
	if !rt.GrantChat(rootSession, writeRoot, "grant_b", "checkpoint-2", nil) {
		t.Fatal("second approval was not recorded")
	}
	if _, ok := rt.RevokeByIDInstalledBy("grant_b", "checkpoint-2"); !ok {
		t.Fatal("second installer could not revoke its grant")
	}
	grants := rt.ListChatGrants(rootSession)
	if len(grants) != 1 || grants[0].ID != "grant_a" || grants[0].SourceCheckpointID != "checkpoint-1" {
		t.Fatalf("first approval changed after rollback: %+v", grants)
	}
}

func TestSandboxWriteRootAdvancedOffAuthorityIsNotListed(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	rt.GrantSessionWriteRoot("coord-sess", t.TempDir())
	if grants := rt.ListChatGrants("coord-sess"); len(grants) != 0 {
		t.Fatalf("implicit Advanced Off authority listed as approval: %+v", grants)
	}
}

func TestSandboxPathGrantRuntimeAbortMint(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	action, _ := rt.Begin(root, "/opt/a", "tc-abort")
	if action != approvalstate.SandboxAskMint {
		t.Fatalf("Begin: %v", action)
	}
	rt.AbortMint(root, "tc-abort")
	action, _ = rt.Begin(root, "/opt/a", "tc-abort")
	if action != approvalstate.SandboxAskMint {
		t.Fatalf("after AbortMint want Mint, got %v", action)
	}
}
