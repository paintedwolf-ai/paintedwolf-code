//go:build linux

package hostread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func fixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "desktop-reader")
	testutil.FailErr(t, "write native reader fixture", os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700))
	return path
}

func TestFixedReaderBoundsOutputAndRetainsOnlySuccessfulSettings(t *testing.T) {
	raw, code, err := read(t.Context(), fixture(t, "printf '%s' \"$1 $2\"\n"))
	testutil.FailErr(t, "read settings", err)
	if code != 0 || string(raw) != "list-recursively org.gnome.system.proxy" {
		t.Fatalf("result = %q, %d", raw, code)
	}
	raw, code, err = read(t.Context(), fixture(t, "printf private-diagnostic >&2; printf partial; exit 2\n"))
	testutil.FailErr(t, "read absent schema", err)
	if code != 2 || raw != nil {
		t.Fatalf("partial settings retained: %q, %d", raw, code)
	}
	_, _, err = read(t.Context(), fixture(t, "head -c 70000 /dev/zero\n"))
	if !errors.Is(err, errOutputLimit) {
		t.Fatalf("oversized settings = %v", err)
	}
}

// processRunning treats a zombie as stopped: an unreaped orphan is not running.
func processRunning(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	state := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+1:])
	return len(state) > 0 && state[0] != "Z"
}

func TestFixedReaderCancellationAndStartFailureAreBoundedAndPrivate(t *testing.T) {
	descendant := filepath.Join(t.TempDir(), "descendant")
	program := fixture(t, "sleep 600 &\necho $! > '"+descendant+"'\nwait\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := read(ctx, program)
		done <- err
	}()
	// The reader's own deadline may end a very slow start before the fixture
	// records its descendant; either ending must leave no descendant running.
	var err error
	returned := false
	testutil.WaitFor(t, func() bool {
		select {
		case err = <-done:
			returned = true
			return true
		default:
		}
		raw, readErr := os.ReadFile(descendant)
		return readErr == nil && len(raw) > 0
	})
	cancel()
	if !returned {
		err = testutil.Receive(t, "reader cancellation", done)
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reader cancellation = %v", err)
	}
	raw, _ := os.ReadFile(descendant)
	if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
		testutil.WaitFor(t, func() bool { return !processRunning(pid) })
	}
	_, _, err = read(t.Context(), "/nonexistent/private-reader-secret")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("reader error leaked its launch input: %v", err)
	}
}

func TestPackagedReaderUsesOnlyItsBuildBoundPath(t *testing.T) {
	previous := gsettingsExecutable
	t.Cleanup(func() { gsettingsExecutable = previous })
	gsettingsExecutable = fixture(t, "printf packaged\n")
	raw, code, err := DesktopProxySettings(t.Context())
	testutil.FailErr(t, "read packaged settings", err)
	if code != 0 || string(raw) != "packaged" {
		t.Fatalf("packaged reader = %q, %d", raw, code)
	}
	for _, path := range []string{"relative-reader", "/nonexistent/private-reader-secret"} {
		gsettingsExecutable = path
		_, _, err := DesktopProxySettings(t.Context())
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid packaged reader = %v", err)
		}
	}
}
