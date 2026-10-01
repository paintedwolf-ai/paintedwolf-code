package confine_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveSocketRequestLive(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sock") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "net.Listen", err)
	t.Cleanup(func() { _ = ln.Close() })

	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)
	if grant.ApprovedPath != sockPath {
		t.Fatalf("approved = %q want %q", grant.ApprovedPath, sockPath)
	}
	resolved, err := filepath.EvalSymlinks(sockPath)
	testutil.FailErr(t, "EvalSymlinks", err)
	if grant.ResolvedPath != resolved {
		t.Fatalf("resolved = %q want %q", grant.ResolvedPath, resolved)
	}
}

func TestRevalidateSocketGrantChanged(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sock") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "net.Listen", err)

	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)
	testutil.FailErr(t, "RevalidateSocketGrant", confine.RevalidateSocketGrant(grant))

	_ = ln.Close()
	_ = os.Remove(sockPath)
	if err := confine.RevalidateSocketGrant(grant); err == nil {
		t.Fatal("expected changed error after socket removal")
	}
}
