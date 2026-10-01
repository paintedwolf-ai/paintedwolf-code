package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

func TestLoopbackRuntimeIsIndependentFromListenRuntime(t *testing.T) {
	t.Parallel()
	listen := approvalstate.NewSandboxPortGrantRuntime()
	connect := approvalstate.NewSandboxPortGrantRuntime()
	listen.GrantChat("chat", []uint16{8123}, "listen", "cp1", nil)
	if granted, _ := connect.SessionPorts("chat"); granted {
		t.Fatal("listener authority leaked into local client authority")
	}
	connect.GrantChat("chat", []uint16{8123}, "connect", "cp2", nil)
	granted, ports := connect.SessionPorts("chat")
	if !granted || len(ports) != 1 || ports[0] != 8123 {
		t.Fatalf("loopback lease: granted=%v ports=%v", granted, ports)
	}
	if _, ok := connect.RevokeByID("connect"); !ok {
		t.Fatal("loopback revoke failed")
	}
	if granted, _ := listen.SessionPorts("chat"); !granted {
		t.Fatal("connection revocation removed listener authority")
	}
	connect.RecordDenied("chat", "8123")
	if begin, _ := listen.Begin("chat", "8123", "listen-call"); begin != approvalstate.SandboxAskMint {
		t.Fatalf("connection denial blocked listener review: %v", begin)
	}
}
