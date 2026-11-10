package nfsserver_test

import (
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
	"testing"
)

func TestReleasedExportHandlesNeverRegainAuthority(t *testing.T) {
	env := newTestEnv(t, nil)
	capability := env.createViewAndExport(t, "reused-view", true)
	var revoked [][]byte
	var previous *rpcClient
	for cycle := range 3 {
		current := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
		status, root := current.mount(capability.ExportPath)
		if status != nfs.MountStatusOk {
			t.Fatalf("cycle %d mount: %v", cycle, status)
		}
		status, child, _ := current.lookup(root, "README.md")
		if status != nfs.NFSStatusOk {
			t.Fatalf("cycle %d child lookup: %v", cycle, status)
		}
		// Adding another root to this active view must not rotate its capabilities.
		other, err := env.Server.RegisterExport("reused-view", nfsserver.WorktreeRoot, "")
		if err != nil {
			t.Fatalf("register second root: %v", err)
		}
		second := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
		if status, _ := second.mount(other.ExportPath); status != nfs.MountStatusOk {
			t.Fatalf("second root mount: %v", status)
		}
		reconnect := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
		for _, client := range []*rpcClient{current, reconnect} {
			if status, body, _ := client.read(child, 0, 64); status != nfs.NFSStatusOk || string(body) != "hello world\n" {
				t.Fatalf("active-view read/reconnect failed: %v %q", status, body)
			}
		}
		if previous != nil {
			fresh := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
			for _, client := range []*rpcClient{previous, fresh} {
				for _, handle := range revoked {
					if status, _ := client.getAttr(handle); status == nfs.NFSStatusOk {
						t.Errorf("cycle %d revived a revoked handle", cycle)
					}
				}
			}
		}
		env.Server.ReleaseExport("reused-view")
		for _, handle := range [][]byte{root, child} {
			if status, _ := current.getAttr(handle); status == nfs.NFSStatusOk {
				t.Fatal("released handle retained authority")
			}
			revoked = append(revoked, handle)
		}
		previous = current
		if cycle < 2 {
			capability, err = env.Server.RegisterExport("reused-view", nfsserver.ProjectRoot, "")
			if err != nil {
				t.Fatalf("register replacement: %v", err)
			}
		}
	}
}
