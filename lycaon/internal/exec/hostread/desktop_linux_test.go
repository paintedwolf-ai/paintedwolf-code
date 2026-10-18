//go:build linux

package hostread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestFixedReaderCancellationAndStartFailureAreBoundedAndPrivate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := read(ctx, fixture(t, "sleep 30 & wait\n"))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("reader cancellation = %v after %v", err, time.Since(started))
	}
	_, _, err = read(t.Context(), "/nonexistent/private-reader-secret")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("reader error leaked its launch input: %v", err)
	}
}
