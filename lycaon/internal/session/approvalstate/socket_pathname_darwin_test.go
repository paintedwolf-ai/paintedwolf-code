//go:build darwin

package approvalstate

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKernelPathnameIntentCannotBecomeProcessBoundAuthority(t *testing.T) {
	path, capture := ownedSocketFixture(t)
	resolved, err := filepath.EvalSymlinks(path)
	testutil.FailErr(t, "resolve saved socket intent", err)
	for _, binding := range []socketref.Binding{{}, {Enforcement: socketref.KernelPathname}} {
		runtime := NewSocketCapabilityRuntime()
		defer runtime.Close()
		saved := SocketChatGrant{ID: "saved-path", ApprovedPath: path, ResolvedPath: resolved, Binding: binding}
		if !runtime.RestoreChatIntent("root", saved) {
			t.Fatal("saved pathname intent was discarded")
		}
		if len(runtime.AppliedGrants("root")) != 0 {
			t.Fatal("saved pathname intent acquired retained authority")
		}
		rows := runtime.ListChatGrants("root")
		if len(rows) != 1 || !rows[0].Unavailable || rows[0].Binding.Mode() != socketref.KernelPathname {
			t.Fatalf("saved interpretation changed: %+v", rows)
		}
		testutil.FailErr(t, "rename original service", os.Rename(path, path+"-old"))
		replacement, err := net.Listen("unix", path)
		testutil.FailErr(t, "restart service at saved name", err)
		if len(runtime.AppliedGrants("root")) != 0 {
			t.Fatal("service restart revived pathname authority")
		}
		grant, owner := capture()
		if grant.Binding.Mode() != socketref.RetainedObject || grant.Binding.ID == "" {
			t.Fatal("fresh approval has no retained recipient")
		}
		if !runtime.GrantChat("root", grant, "fresh-instance", "checkpoint", "digest", nil) {
			t.Fatal("fresh retained approval was refused")
		}
		owner.Close()
		if applied := runtime.AppliedGrants("root"); len(applied) != 1 || applied[0].Binding != grant.Binding {
			t.Fatalf("fresh approval is unavailable: %+v", applied)
		}
		runtime.Close()
		testutil.FailErr(t, "close replacement service", replacement.Close())
		testutil.FailErr(t, "restore original fixture name", os.Rename(path+"-old", path))
	}
}
