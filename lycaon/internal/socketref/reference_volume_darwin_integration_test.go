//go:build darwin && cgo && integration

package socketref

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestRetainedSocketAcrossActualMountedVolumes(t *testing.T) {
	root := endpointFixtureRoot(t)
	image := filepath.Join(t.TempDir(), "service-volume.sparseimage")
	run := func(args ...string) {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), "/usr/bin/hdiutil", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("prepare owned service volume: %v: %s", err, output)
		}
	}
	run("create", "-quiet", "-size", "128m", "-fs", "APFS", "-type", "SPARSE", "-volname", "Painted Wolf socket fixture", image)
	mount := filepath.Join(root, "volume")
	testutil.FailErr(t, "create owned volume mountpoint", os.Mkdir(mount, 0700))
	var original unix.Stat_t
	testutil.FailErr(t, "inspect mountpoint before attach", unix.Stat(mount, &original))
	t.Cleanup(func() {
		var current unix.Stat_t
		if unix.Stat(mount, &current) != nil || current.Dev == original.Dev {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "detach", mount).CombinedOutput()
		if err != nil {
			t.Errorf("detach owned socket fixture volume: %v: %s", err, output)
		}
	})
	run("attach", "-quiet", "-nobrowse", "-mountpoint", mount, image)
	var mounted unix.Stat_t
	testutil.FailErr(t, "inspect attached service volume", unix.Stat(mount, &mounted))
	if mounted.Dev == original.Dev {
		t.Fatal("fixture did not create a distinct filesystem")
	}
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(mount, "service")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	testutil.FailErr(t, "listen on separate service volume", err)
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_, err = fmt.Fprintln(conn, "separate filesystem")
		done <- err
	}()
	owner, err := Capture(path)
	testutil.FailErr(t, "retain service across volumes", err)
	t.Cleanup(owner.Close)
	ref, release, err := owner.operation()
	testutil.FailErr(t, "inspect retained volume authority", err)
	var retained unix.Stat_t
	testutil.FailErr(t, "inspect retained directory filesystem", unix.Fstat(int(ref.file.Fd()), &retained))
	release()
	if retained.Dev != mounted.Dev {
		t.Fatalf("service object was not retained on its own filesystem: service=%d, retained=%d", mounted.Dev, retained.Dev)
	}
	endpoint, err := owner.Endpoint("unix")
	testutil.FailErr(t, "bind separate-volume service endpoint", err)
	client := endpointClient(t, endpoint)
	response, err := bufio.NewReader(client).ReadString('\n')
	testutil.FailErr(t, "read service across volumes", err)
	if response != "separate filesystem\n" {
		t.Fatalf("cross-volume service response changed: %q", response)
	}
	testutil.FailErr(t, "serve cross-volume response", <-done)
	_, err = io.Copy(io.Discard, client)
	testutil.FailErr(t, "drain cross-volume response", err)
}
