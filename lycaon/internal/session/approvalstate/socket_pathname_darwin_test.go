//go:build darwin

package approvalstate

import (
	"net"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKernelPathnameIntentSurvivesServiceAbsenceAndRestart(t *testing.T) {
	path, _ := ownedSocketFixture(t)
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "kernel pathname grant", err)
	if grant.Binding.Mode() != socketref.KernelPathname {
		t.Fatal("Mac command did not use pathname realization")
	}
	runtime := NewSocketCapabilityRuntime()
	defer runtime.Close()
	if !runtime.GrantChat("root", grant, "pathname", "checkpoint", "digest", nil) {
		t.Fatal("install pathname grant")
	}
	testutil.FailErr(t, "stop serving approved name", os.Rename(path, path+"-old"))
	if len(runtime.AppliedGrants("root")) != 0 {
		t.Fatal("absent path was usable")
	}
	rows := runtime.ListChatGrants("root")
	if len(rows) != 1 || rows[0].Unavailable {
		t.Fatal("service absence retired pathname intent")
	}
	restarted := NewSocketCapabilityRuntime()
	defer restarted.Close()
	rows[0].Unavailable = true
	if !restarted.RestoreChatIntent("root", rows[0]) {
		t.Fatal("restore pathname intent while daemon absent")
	}
	if len(restarted.AppliedGrants("root")) != 0 {
		t.Fatal("restored absent name was usable")
	}
	replacement, err := net.Listen("unix", path)
	testutil.FailErr(t, "same-path daemon restart", err)
	defer replacement.Close()
	for _, owner := range []*SocketCapabilityRuntime{runtime, restarted} {
		applied := owner.AppliedGrants("root")
		if len(applied) != 1 || applied[0].Binding.Mode() != socketref.KernelPathname || applied[0].Binding.ID != "" {
			t.Fatalf("same-path daemon restart lost pathname permission: %+v", applied)
		}
	}
}
