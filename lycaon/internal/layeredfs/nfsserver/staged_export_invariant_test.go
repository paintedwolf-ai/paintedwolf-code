package nfsserver_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestStagedExportActivationDoesNotGrantSiblingAuthority(t *testing.T) {
	env := newTestEnv(t, nil)
	primary, err := env.Server.RegisterStagedExport("default", nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("stage primary: %v", err)
	}
	sibling, err := env.Server.RegisterStagedExport("default", nfsserver.WorktreeRoot, "")
	if err != nil {
		t.Fatalf("stage sibling: %v", err)
	}
	if err := env.Server.ActivateExport(primary.ExportPath); err == nil {
		t.Fatal("activated before mount exchange")
	}
	client := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	mountStatus, root := client.mount(primary.ExportPath)
	if mountStatus != nfs.MountStatusOk {
		t.Fatalf("primary mount: %v", mountStatus)
	}
	other := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	mountStatus, otherRoot := other.mount(sibling.ExportPath)
	if mountStatus != nfs.MountStatusOk {
		t.Fatalf("sibling mount: %v", mountStatus)
	}
	if status, _ := client.getAttr(root); status != nfs.NFSStatusOk {
		t.Fatalf("bootstrap root metadata: %v", status)
	}
	if status, _, _ := client.lookup(root, "README.md"); status == nfs.NFSStatusOk {
		t.Error("staged export disclosed child handle")
	}
	if status, _ := client.readdir(root, 0, [8]byte{}, 4096); status == nfs.NFSStatusOk {
		t.Error("staged export disclosed namespace")
	}
	if status, _ := client.mkdir(root, "denied-before-activation"); status == nfs.NFSStatusOk {
		t.Error("staged export permitted mutation")
	}
	if err := env.Server.ActivateExport(primary.ExportPath); err != nil {
		t.Fatalf("activate primary: %v", err)
	}
	// A late bootstrap replay must not revoke the legitimate active export.
	replay := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	if status, _ := replay.mount(primary.ExportPath); status == nfs.MountStatusOk {
		t.Error("bootstrap token reused")
	}
	fresh := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	status, child, _ := fresh.lookup(root, "README.md")
	if status != nfs.NFSStatusOk {
		t.Fatalf("active lookup after replay: %v", status)
	}
	if status, body, _ := fresh.read(child, 0, 64); status != nfs.NFSStatusOk || string(body) != "hello world\n" {
		t.Fatalf("active content: %v %q", status, body)
	}
	if status, _, _ := fresh.lookup(root, "denied-before-activation"); status != nfs.NFSStatusNoEnt {
		t.Errorf("denied mutation left a directory: %v", status)
	}
	// The second handle must be denied before namespace operations inspect it.
	if status := fresh.rename(root, "README.md", otherRoot, "stolen"); status != nfs.NFSStatusAccess {
		t.Errorf("rename into staged root did not deny authority: %v", status)
	}
	if status, _ := other.mkdir(otherRoot, "denied-sibling"); status == nfs.NFSStatusOk {
		t.Error("primary activation granted sibling write authority")
	}
	if err := env.Server.ActivateExport(sibling.ExportPath); err != nil {
		t.Fatalf("activate sibling: %v", err)
	}
	if status, _, _ := other.lookup(otherRoot, "denied-sibling"); status != nfs.NFSStatusNoEnt {
		t.Errorf("denied sibling mutation left a directory: %v", status)
	}
	if status, _ := other.mkdir(otherRoot, "allowed-sibling"); status != nfs.NFSStatusOk {
		t.Fatalf("activated sibling unusable: %v", status)
	}
	env.Server.ReleaseExport("default")
	if err := env.Server.ActivateExport(primary.ExportPath); err == nil {
		t.Fatal("reactivated revoked export")
	}
	if status, _ := fresh.getAttr(root); status == nfs.NFSStatusOk {
		t.Error("revoked active handle survived")
	}
}
