package approvalstate_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
)

func tempSocket(t *testing.T) (approved, resolved string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	resolved, err = filepath.EvalSymlinks(path)
	testutil.FailErr(t, "eval symlinks", err)
	return path, resolved
}

func TestSocketCapabilityRuntimeTimeBoundExpires(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	root := "coord-root"
	approved, resolved := tempSocket(t)
	grant := confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}

	// A live time bound authorizes at the boundary.
	future := time.Now().UTC().Add(time.Hour)
	rt.GrantChat(root, grant, "grant_ttl", "cp-ttl", "digest-a", &future)
	if got := rt.AppliedGrants(root); len(got) != 1 {
		t.Fatalf("live time-bound grant absent from boundary: %+v", got)
	}

	rt.RevokeByID("grant_ttl")
	past := time.Now().UTC().Add(-time.Minute)
	rt.GrantChat(root, grant, "grant_ttl", "cp-ttl", "digest-a", &past)
	if got := rt.AppliedGrants(root); len(got) != 0 {
		t.Fatalf("expired time-bound grant still authorizes: %+v", got)
	}
	// Expired rows remain available for review.
	if rows := rt.ListChatGrants(root); len(rows) != 1 || rows[0].ExpiresAt == nil {
		t.Fatalf("ListChatGrants = %+v", rows)
	}
}

func TestSocketTaskGrantKeepsSeparateInstallers(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	approved, resolved := tempSocket(t)
	grant := confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}
	if !rt.GrantChat("root", grant, "grant_a", "cp-1", "digest-a", nil) {
		t.Fatal("initial grant was not stored")
	}
	if !rt.GrantChat("root", grant, "grant_b", "cp-2", "digest-b", nil) {
		t.Fatal("second approval was not recorded")
	}
	if _, ok := rt.RevokeByIDInstalledBy("grant_b", "cp-2"); !ok {
		t.Fatal("second installer could not revoke its grant")
	}
	rows := rt.ListChatGrants("root")
	if len(rows) != 1 || rows[0].ID != "grant_a" || rows[0].SourceCheckpointID != "cp-1" {
		t.Fatalf("first approval changed after rollback: %+v", rows)
	}
}

func TestSocketCapabilityRuntimeTaskGrantRevalidate(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	root := "coord-root"
	approved, resolved := tempSocket(t)

	rt.GrantChat(root, confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}, "grant_t", "cp-1", "digest-a", nil)
	got := rt.AppliedGrants(root)
	if len(got) != 1 || got[0].ResolvedPath != resolved {
		t.Fatalf("AppliedGrants = %+v", got)
	}

	if err := os.Remove(approved); err != nil {
		testutil.FailErr(t, "remove socket", err)
	}
	if live := rt.AppliedGrants(root); len(live) != 0 {
		t.Fatalf("stale grant should drop, got %+v", live)
	}
}

func TestSocketCapabilityRuntimeApplicationCapAndDedupe(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	root := "coord-root"
	base, err := os.MkdirTemp("", "skcap") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	grants := make([]confine.SocketGrant, 0, confine.MaxSocketGrants+2)
	listeners := make([]net.Listener, 0, confine.MaxSocketGrants+2)
	t.Cleanup(func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	})
	for i := 0; i < confine.MaxSocketGrants+2; i++ {
		path := filepath.Join(base, fmt.Sprintf("s%02d.sock", i))
		_ = os.Remove(path)
		ln, err := net.Listen("unix", path)
		testutil.FailErr(t, "listen", err)
		listeners = append(listeners, ln)
		resolved, err := filepath.EvalSymlinks(path)
		testutil.FailErr(t, "eval", err)
		g := confine.SocketGrant{ApprovedPath: path, ResolvedPath: resolved}
		grants = append(grants, g)
		rt.GrantChat(root, g, "grant_t", "cp", "d", nil)
	}
	if len(rt.AppliedGrants(root)) != confine.MaxSocketGrants {
		t.Fatalf("applied = %d want the per-execution cap %d", len(rt.AppliedGrants(root)), confine.MaxSocketGrants)
	}
	if got := len(rt.ListChatGrants(root)); got != confine.MaxSocketGrants+2 {
		t.Fatalf("approved grants held = %d, want every approval", got)
	}
	rt.GrantChat(root, grants[0], "grant_t", "cp2", "d2", nil)
	if got := len(rt.ListChatGrants(root)); got != confine.MaxSocketGrants+2 {
		t.Fatalf("re-grant of a live pair must not duplicate it, got %d", got)
	}
}

func TestSocketCapabilityRuntimeRevokeByID(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	root := "coord-root"
	approved, resolved := tempSocket(t)
	rt.GrantChat(root, confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}, "grant_t", "cp", "d", nil)
	if _, ok := rt.RevokeByID("grant_t"); !ok {
		t.Fatal("revoke found no grant")
	}
	if len(rt.AppliedGrants(root)) != 0 {
		t.Fatalf("revoke must drop the grant, got %+v", rt.AppliedGrants(root))
	}
	if _, ok := rt.RevokeByID("grant_t"); ok {
		t.Fatal("second revoke found a grant")
	}
}

func TestSocketCapabilityRuntimePermitOnce(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	approved, resolved := tempSocket(t)
	g := confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}
	rt.IssuePermit("sess-1", "tc-1", "digest-1", g)
	ok, err := rt.ConsumePermit("sess-1", "tc-1", "digest-1", g)
	testutil.FailErr(t, "ConsumePermit", err)
	if !ok {
		t.Fatal("first consume must succeed")
	}
	ok, err = rt.ConsumePermit("sess-1", "tc-1", "digest-1", g)
	if ok || err != nil {
		t.Fatalf("second consume must fail: ok=%v err=%v", ok, err)
	}
}

func TestSocketCapabilityRuntimeAuthorizedGrants(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	root := "coord-root"
	aApproved, aResolved := tempSocket(t)
	bApproved, bResolved := tempSocket(t)
	ga := confine.SocketGrant{ApprovedPath: aApproved, ResolvedPath: aResolved}
	gb := confine.SocketGrant{ApprovedPath: bApproved, ResolvedPath: bResolved}
	rt.GrantChat(root, ga, "grant_t", "cp", "d", nil)
	rt.IssuePermit("worker-1", "tc-2", "digest-2", gb)

	got := rt.AuthorizedGrants(root, "worker-1", "tc-2", "digest-2", []confine.SocketGrant{ga, gb})
	if len(got) != 2 {
		t.Fatalf("AuthorizedGrants = %+v", got)
	}
}

func TestSocketCapabilityRuntimeListAllChatGrants(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	approvedA, resolvedA := tempSocket(t)
	approvedB, resolvedB := tempSocket(t)
	rt.GrantChat("root-a", confine.SocketGrant{ApprovedPath: approvedA, ResolvedPath: resolvedA}, "grant_a", "cp", "d", nil)
	rt.GrantChat("root-b", confine.SocketGrant{ApprovedPath: approvedB, ResolvedPath: resolvedB}, "grant_b", "cp", "d", nil)

	all := rt.ListAllChatGrants()
	if len(all) != 2 {
		t.Fatalf("ListAllChatGrants roots = %+v want 2", all)
	}
	if len(all["root-a"]) != 1 || all["root-a"][0].ID != "grant_a" {
		t.Fatalf("root-a grants = %+v", all["root-a"])
	}
	if len(all["root-b"]) != 1 || all["root-b"][0].ID != "grant_b" {
		t.Fatalf("root-b grants = %+v", all["root-b"])
	}

	rt.ForgetSession("root-a")
	all = rt.ListAllChatGrants()
	if len(all) != 1 || len(all["root-b"]) != 1 {
		t.Fatalf("after forget, ListAllChatGrants = %+v want only root-b", all)
	}
}

func TestSocketCapabilityRuntimeForgetSession(t *testing.T) {
	rt := approvalstate.NewSocketCapabilityRuntime()
	root := "coord-root"
	approved, resolved := tempSocket(t)
	g := confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}
	rt.GrantChat(root, g, "grant_t", "cp", "d", nil)
	rt.IssuePermit(root, "tc-1", "digest-1", g)
	rt.ForgetSession(root)
	if len(rt.AppliedGrants(root)) != 0 {
		t.Fatal("ForgetSession must clear task grants")
	}
	ok, _ := rt.ConsumePermit(root, "tc-1", "digest-1", g)
	if ok {
		t.Fatal("ForgetSession must clear permits")
	}
}
