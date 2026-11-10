package nfsserver_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestReleasedExportHandlesNeverRegainAuthority(t *testing.T) {
	env := newTestEnv(t, nil)
	capability := env.createViewAndExport(t, "reused-view", true)
	original := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	status, oldRoot := original.mount(capability.ExportPath)
	if status != nfs.MountStatusOk {
		t.Fatalf("initial mount: %v", status)
	}
	env.Server.ReleaseExport("reused-view")
	if status, _ := original.getAttr(oldRoot); status == nfs.NFSStatusOk {
		t.Fatal("released root retained authority")
	}
	// A new capability for the same view must not revive its revoked handles.
	replacement, err := env.Server.RegisterExport("reused-view", nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("register replacement capability: %v", err)
	}
	current := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	status, newRoot := current.mount(replacement.ExportPath)
	if status != nfs.MountStatusOk {
		t.Fatalf("replacement mount: %v", status)
	}
	if status, _ := current.getAttr(newRoot); status != nfs.NFSStatusOk {
		t.Fatalf("replacement root unusable: %v", status)
	}
	for _, client := range []*rpcClient{original, dialRPC(t, env.Server.Addr(), env.UID, env.GID)} {
		if status, _ := client.getAttr(oldRoot); status == nfs.NFSStatusOk {
			t.Error("new export revived a revoked root handle")
		}
	}
}
