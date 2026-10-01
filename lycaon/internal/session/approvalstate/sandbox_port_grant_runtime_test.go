package approvalstate

import (
	"testing"
)

func TestPortGrantRuntimeLeaseSemantics(t *testing.T) {
	t.Parallel()
	rt := NewSandboxPortGrantRuntime()

	granted, _ := rt.SessionPorts("chat")
	if granted {
		t.Fatal("empty runtime reports a grant")
	}

	rt.GrantChat("chat", []uint16{8000}, "grant_a", "cp1", nil)
	granted, ports := rt.SessionPorts("chat")
	if !granted || len(ports) != 1 || ports[0] != 8000 {
		t.Fatalf("port grant: granted=%v ports=%v", granted, ports)
	}

	rt.GrantChat("chat", []uint16{9000}, "grant_b", "cp2", nil)
	_, ports = rt.SessionPorts("chat")
	if len(ports) != 2 || ports[0] != 8000 || ports[1] != 9000 {
		t.Fatalf("union of port grants: %v", ports)
	}

	// One unnarrowed lease covers any port.
	rt.GrantChat("chat", nil, "grant_c", "cp3", nil)
	granted, ports = rt.SessionPorts("chat")
	if !granted || ports != nil {
		t.Fatalf("unnarrowed lease: granted=%v ports=%v", granted, ports)
	}

	// Revocation by id restores the narrowed view.
	if _, ok := rt.RevokeByID("grant_c"); !ok {
		t.Fatal("revoke grant_c failed")
	}
	_, ports = rt.SessionPorts("chat")
	if len(ports) != 2 {
		t.Fatalf("after revoke: %v", ports)
	}

	// Other session trees are untouched.
	if granted, _ := rt.SessionPorts("other"); granted {
		t.Fatal("grant leaked across session trees")
	}

	rt.ForgetSession("chat")
	if granted, _ := rt.SessionPorts("chat"); granted {
		t.Fatal("ForgetSession left a live lease")
	}
}

func TestPortTaskGrantKeepsSeparateInstallers(t *testing.T) {
	t.Parallel()
	rt := NewSandboxPortGrantRuntime()
	if !rt.GrantChat("chat", []uint16{8000}, "grant_a", "cp1", nil) {
		t.Fatal("initial grant was not stored")
	}
	if !rt.GrantChat("chat", []uint16{8000}, "grant_b", "cp2", nil) {
		t.Fatal("second approval was not recorded")
	}
	if _, ok := rt.RevokeByIDInstalledBy("grant_b", "cp2"); !ok {
		t.Fatal("second installer could not revoke its grant")
	}
	grants := rt.ListChatGrants("chat")
	if len(grants) != 1 || grants[0].ID != "grant_a" || grants[0].SourceCheckpointID != "cp1" {
		t.Fatalf("first approval changed after rollback: %+v", grants)
	}
}

func TestPortGrantRuntimeGuards(t *testing.T) {
	t.Parallel()
	rt := NewSandboxPortGrantRuntime()
	key := PortGrantKey([]uint16{8000})

	action, _ := rt.Begin("chat", key, "call1")
	if action != SandboxAskMint {
		t.Fatalf("first ask should mint, got %v", action)
	}
	rt.RegisterPending("chat", key, "cp1")
	if action, id := rt.Begin("chat", key, "call2"); action != SandboxAskJoin || id != "cp1" {
		t.Fatalf("concurrent ask should join cp1, got %v %q", action, id)
	}
	rt.Finish("chat", key, true)
	if action, _ := rt.Begin("chat", key, "call3"); action != SandboxAskSkipDenied {
		t.Fatalf("denied key should skip, got %v", action)
	}
	// A new user turn clears the deny-set.
	rt.NoteUserIntentBoundary("chat")
	if action, _ := rt.Begin("chat", key, "call4"); action != SandboxAskMint {
		t.Fatalf("cleared deny-set should mint, got %v", action)
	}
	// The same tool call never re-enters.
	if action, _ := rt.Begin("chat", key, "call4"); action != SandboxAskSkipToolCall {
		t.Fatalf("duplicate tool call should skip, got %v", action)
	}
}

func TestPortGrantKey(t *testing.T) {
	t.Parallel()
	if got := PortGrantKey(nil); got != "any" {
		t.Fatalf("nil ports key %q", got)
	}
	if got := PortGrantKey([]uint16{9000, 8000}); got != "8000,9000" {
		t.Fatalf("sorted key %q", got)
	}
}
