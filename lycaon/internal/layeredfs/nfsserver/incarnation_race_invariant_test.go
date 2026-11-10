package nfsserver_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestInFlightRequestsCannotMintReplacementCapabilities(t *testing.T) {
	for _, operation := range []string{"mount", "lookup"} {
		t.Run(operation, func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			var paused atomic.Bool
			var unblock sync.Once
			releaseRequest := func() { unblock.Do(func() { close(resume) }) }
			defer releaseRequest()
			hook := func() error {
				if paused.CompareAndSwap(false, true) {
					close(entered)
					<-resume
				}
				return nil
			}
			faults := &nfsserver.FaultInjector{}
			if operation == "mount" {
				faults.OnMount = func(nfsserver.ViewID, nfsserver.RootID) error { return hook() }
			} else {
				faults.OnLookup = func(nfsserver.ViewID, layeredfs.QualifiedPath) error { return hook() }
			}
			env := newTestEnv(t, faults)
			cap := env.createViewAndExport(t, "reused-race", true)
			old := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
			var root []byte
			if operation == "lookup" {
				status, handle := old.mount(cap.ExportPath)
				if status != nfs.MountStatusOk {
					t.Fatalf("initial mount: %v", status)
				}
				root = handle
			}
			result := make(chan bool, 1)
			go func() {
				if operation == "mount" {
					status, _ := old.mount(cap.ExportPath)
					result <- status == nfs.MountStatusOk
				} else {
					status, _, _ := old.lookup(root, "README.md")
					result <- status == nfs.NFSStatusOk
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("request did not reach issuance boundary")
			}
			// The old request has authenticated but has not issued its handle. Replace
			// the registration before allowing it to finish; there is no timing race.
			env.Server.ReleaseExport("reused-race")
			replacement, err := env.Server.RegisterExport("reused-race", nfsserver.ProjectRoot, "")
			if err != nil {
				t.Fatalf("register replacement: %v", err)
			}
			releaseRequest()
			select {
			case success := <-result:
				if success {
					t.Error("old request minted a capability after replacement")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("old request did not settle")
			}
			fresh := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
			status, newRoot := fresh.mount(replacement.ExportPath)
			if status != nfs.MountStatusOk {
				t.Fatalf("replacement mount: %v", status)
			}
			ns, child, _ := fresh.lookup(newRoot, "README.md")
			if ns != nfs.NFSStatusOk {
				t.Fatalf("replacement lookup: %v", ns)
			}
			if ns, body, _ := fresh.read(child, 0, 64); ns != nfs.NFSStatusOk || string(body) != "hello world\n" {
				t.Fatalf("replacement content: %v %q", ns, body)
			}
		})
	}
}
