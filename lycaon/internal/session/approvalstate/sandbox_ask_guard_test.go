package approvalstate

import (
	"testing"
)

// Matching calls join an open card; settled cards release the reservation.
func TestAskGuardCoalescesAcrossBothRuntimes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		gate func() AskGate
		key  string
	}{
		{"path grants", func() AskGate { return NewSandboxPathGrantRuntime() }, "/tmp/root"},
		{"listen leases", func() AskGate { return NewSandboxPortGrantRuntime() }, "3000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate := tc.gate()

			if begin, _ := gate.Begin("root", tc.key, "call-1"); begin != SandboxAskMint {
				t.Fatalf("first ask = %v, want mint", begin)
			}
			if begin, _ := gate.Begin("root", tc.key, "call-1"); begin != SandboxAskSkipToolCall {
				t.Fatalf("same tool call = %v, want skip", begin)
			}

			gate.RegisterPending("root", tc.key, "cp-1")
			begin, existing := gate.Begin("root", tc.key, "call-2")
			if begin != SandboxAskJoin || existing != "cp-1" {
				t.Fatalf("concurrent ask = %v/%q, want join on cp-1", begin, existing)
			}

			gate.Finish("root", tc.key)
			if begin, _ := gate.Begin("root", tc.key, "call-3"); begin != SandboxAskMint {
				t.Fatalf("after settlement = %v, want mint", begin)
			}
		})
	}
}

func TestAskGuardKeepsCapabilitiesApart(t *testing.T) {
	t.Parallel()
	// One guard per capability: denying a write root leaves a listener ask open.
	paths := NewSandboxPathGrantRuntime()
	listens := NewSandboxPortGrantRuntime()

	paths.Begin("root", "/tmp/root", "call-1")
	paths.Finish("root", "/tmp/root")

	if begin, _ := listens.Begin("root", "any", "call-2"); begin != SandboxAskMint {
		t.Fatalf("listen ask after a write-root denial = %v, want mint", begin)
	}
}

func TestApprovedPathGrantsAreNeverTrimmed(t *testing.T) {
	t.Parallel()
	runtime := NewSandboxPathGrantRuntime()
	for i := 0; i < pathRunGrantCap+8; i++ {
		root := "/tmp/root-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if !runtime.GrantChat("root", root, "grant-"+root, "cp-"+root, nil) {
			t.Fatalf("grant %q refused", root)
		}
		runtime.GrantSessionWriteRoot("root", root+"/run")
	}
	if got := len(runtime.ListChatGrants("root")); got != pathRunGrantCap+8 {
		t.Fatalf("held %d approved grants, want all %d", got, pathRunGrantCap+8)
	}
	if got := len(runtime.SessionWriteRoots("root")); got != 2*pathRunGrantCap+8 {
		t.Fatalf("held %d roots, want every approved root plus the %d newest run roots", got, pathRunGrantCap)
	}
}

func TestReleaseRunKeepsApprovedPathGrants(t *testing.T) {
	t.Parallel()
	runtime := NewSandboxPathGrantRuntime()
	runtime.GrantChat("root", "/tmp/approved", "grant-approved", "cp-1", nil)
	runtime.GrantSessionWriteRoot("root", "/tmp/derived")
	runtime.Begin("root", "/tmp/denied", "call-1")
	runtime.Finish("root", "/tmp/denied")

	runtime.ReleaseRun("root")
	roots := runtime.SessionWriteRoots("root")
	if len(roots) != 1 || roots[0] != "/tmp/approved" {
		t.Fatalf("roots after Stop = %v, want only the approved grant", roots)
	}
	if begin, _ := runtime.Begin("root", "/tmp/denied", "call-2"); begin != SandboxAskMint {
		t.Fatalf("a denial from the stopped run still suppresses the ask: %v", begin)
	}
	runtime.ForgetSession("root")
	if roots := runtime.SessionWriteRoots("root"); len(roots) != 0 {
		t.Fatalf("roots after the chat is disposed = %v", roots)
	}
}
