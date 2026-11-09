package command_test

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExternalSpawnUsesOnlyAdmittedWriteRoots(t *testing.T) {
	requireAppliedBoundary(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "find home", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-grant-") //nolint:usetesting // Must lie outside default temporary write roots.
	testutil.FailErr(t, "create outside directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	approved := filepath.Join(outside, "approved")
	testutil.FailErr(t, "create approved directory", os.Mkdir(approved, 0700))
	f := newProgramFixture(t)
	granted := true
	f.Runtime.Executor.Boundary.SetSessionWriteRootOverlay(func(context.Context, string, string) []string {
		if granted {
			return []string{approved}
		}
		return nil
	})
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{filepath.Join(approved, "result"), true},
		{filepath.Join(outside, "unapproved"), false},
	} {
		out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'printf ok > \"$1\"' sh "+strconv.Quote(tc.path))
		testutil.FailErr(t, "execute write", err)
		if got := decodeOutcome(t, out); (got.Status == 0) != tc.allowed {
			t.Fatalf("write %s allowed=%v: %+v", tc.path, tc.allowed, got)
		}
		data, err := os.ReadFile(tc.path)
		if tc.allowed && (err != nil || string(data) != "ok") || !tc.allowed && !os.IsNotExist(err) {
			t.Fatalf("write effect %s: %q, %v", tc.path, data, err)
		}
	}
	granted = false
	out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'printf changed > \"$1\"' sh "+strconv.Quote(filepath.Join(approved, "result")))
	testutil.FailErr(t, "execute without grant", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("grant escaped its admitted invocation")
	}
	data, err := os.ReadFile(filepath.Join(approved, "result"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("ungranted invocation changed file: %q, %v", data, err)
	}
}
