//go:build linux || darwin

package approvalstate

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/hitl"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func ownedSocketFixture(t *testing.T) (string, func() (confine.SocketGrant, *socketref.Lease)) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "so-") //nolint:usetesting // Unix paths are bounded by the kernel.
	testutil.FailErr(t, "socket fixture directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	path := filepath.Join(root, "socket")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "socket fixture listener", err)
	t.Cleanup(func() { _ = listener.Close() })
	return path, func() (confine.SocketGrant, *socketref.Lease) {
		owner, err := socketref.Capture(path)
		testutil.FailErr(t, "capture socket fixture", err)
		t.Cleanup(owner.Close)
		resolved, err := filepath.EvalSymlinks(path)
		testutil.FailErr(t, "resolve socket fixture", err)
		return confine.SocketGrant{ApprovedPath: path, ResolvedPath: resolved, Binding: owner.Binding()}, owner
	}
}

func assertSocketBinding(t *testing.T, binding socketref.Binding, live bool) {
	t.Helper()
	lease, err := binding.Acquire()
	if live {
		testutil.FailErr(t, "acquire live socket binding", err)
		lease.Close()
	} else if !errors.Is(err, socketref.ErrUnbound) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("retired binding acquire = %v", err)
	}
}

func TestSocketPermitOwnersReleaseAtRealRuntimeBoundaries(t *testing.T) {
	for _, terminal := range []string{"revoke", "consume", "release-root", "forget-root", "forget-child", "close"} {
		t.Run(terminal, func(t *testing.T) {
			_, capture := ownedSocketFixture(t)
			grant, initial := capture()
			runtime := NewSocketCapabilityRuntime()
			defer runtime.Close()
			runtime.IssuePermit("root", hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: "call"}, "digest", grant)
			initial.Close()
			assertSocketBinding(t, grant.Binding, true)
			retained := runtime.permits.Snapshot()
			if len(retained) != 1 {
				t.Fatalf("permits = %d", len(retained))
			}
			switch terminal {
			case "revoke":
				runtime.RevokePermit(hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: "call"}, "digest", grant)
			case "consume":
				ok, err := runtime.ConsumePermit(hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: "call"}, "digest", grant)
				testutil.FailErr(t, "consume permit", err)
				if !ok {
					t.Fatal("permit was not consumed")
				}
			case "release-root":
				runtime.ReleaseRun("root")
			case "forget-root":
				runtime.ForgetSession("root")
			case "forget-child":
				runtime.ForgetSession("child")
			case "close":
				runtime.Close()
			}
			for _, slot := range retained {
				assertClosedSocketLease(t, slot.resource)
			}
			assertSocketBinding(t, grant.Binding, false)
			if len(runtime.permits.Snapshot()) != 0 {
				t.Fatal("terminal permit retained slot")
			}
		})
	}
}

func TestSocketPermitReplacementAndEvictionCloseOwners(t *testing.T) {
	_, capture := ownedSocketFixture(t)
	runtime := NewSocketCapabilityRuntime()
	defer runtime.Close()
	runtime.permits = *scopedstore.New[*socketPermitSlot](2)
	grant, initial := capture()
	runtime.IssuePermit("root", hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: "one"}, "digest", grant)
	initial.Close()
	first := runtime.permits.Snapshot()[socketPermitKey("child", "one", "digest", grant)]
	runtime.IssuePermit("root", hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: "one"}, "digest", grant)
	assertClosedSocketLease(t, first.resource)
	replaced := runtime.permits.Snapshot()[socketPermitKey("child", "one", "digest", grant)]
	for i := range 2 {
		runtime.IssuePermit("root", hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: fmt.Sprintf("next-%d", i)}, "digest", grant)
	}
	assertClosedSocketLease(t, replaced.resource)
	if runtime.permits.Len() != 2 {
		t.Fatalf("permit capacity = %d", runtime.permits.Len())
	}
}

func TestSocketChatExpiryAndReplacementPreserveIntent(t *testing.T) {
	path, capture := ownedSocketFixture(t)
	runtime := NewSocketCapabilityRuntime()
	defer runtime.Close()
	old, oldOwner := capture()
	expiry := time.Now().Add(time.Hour)
	deadline := expiry
	if !runtime.GrantChat("root", old, "old", "checkpoint", "digest", &expiry) {
		t.Fatal("install chat grant")
	}
	oldOwner.Close()
	expiry = time.Now().Add(2 * time.Hour)
	row := runtime.guard.listGrants("root")[0]
	if !row.ExpiresAt.Equal(deadline) {
		t.Fatal("caller changed authoritative expiry")
	}
	exported := runtime.ListChatGrants("root")
	*exported[0].ExpiresAt = expiry
	if !runtime.guard.listGrants("root")[0].ExpiresAt.Equal(deadline) {
		t.Fatal("reader changed authoritative expiry")
	}
	runtime.expireSocketOwners(deadline)
	assertClosedSocketLease(t, row.resource)
	assertSocketBinding(t, old.Binding, false)
	if len(runtime.ListChatGrants("root")) != 1 {
		t.Fatal("expiry discarded intent")
	}
	if err := os.Rename(path, path+"-old"); err != nil {
		testutil.FailErr(t, "move original socket", err)
	}
	replacement, err := net.Listen("unix", path)
	testutil.FailErr(t, "replacement listener", err)
	defer replacement.Close()
	fresh, freshOwner := capture()
	if !runtime.GrantChat("root", fresh, "fresh", "new-checkpoint", "new-digest", nil) {
		t.Fatal("fresh approval not installed")
	}
	freshOwner.Close()
	rows := runtime.ListChatGrants("root")
	if len(rows) != 2 || !rows[0].Unavailable {
		t.Fatalf("replacement lost stale intent: %+v", rows)
	}
	if got := runtime.AppliedGrants("root"); len(got) != 1 || got[0].Binding != fresh.Binding {
		t.Fatalf("applied grants = %+v", got)
	}
}

func assertClosedSocketLease(t *testing.T, lease *socketref.Lease) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	connection, err := lease.DialContext(ctx)
	if connection != nil {
		_ = connection.Close()
	}
	if !errors.Is(err, socketref.ErrUnbound) {
		t.Fatalf("retired owner remains usable: %v", err)
	}
}

func TestSocketPermitCapacityBoundsDistinctLiveReferencesWithoutGC(t *testing.T) {
	_, capture := ownedSocketFixture(t)
	runtime := NewSocketCapabilityRuntime()
	defer runtime.Close()
	runtime.permits = *scopedstore.New[*socketPermitSlot](2)
	var prior []socketref.Binding
	for i := range 256 {
		grant, owner := capture()
		runtime.IssuePermit("root", hitl.EffectPermitIdentity{SessionID: "child", ToolCallID: fmt.Sprint(i)}, "digest", grant)
		owner.Close()
		prior = append(prior, grant.Binding)
		assertSocketBinding(t, grant.Binding, true)
		if i >= 2 {
			assertSocketBinding(t, prior[i-2], false)
		}
		if runtime.permits.Len() > 2 {
			t.Fatal("permit references exceeded capacity")
		}
	}
	runtime.Close()
	for _, binding := range prior {
		assertSocketBinding(t, binding, false)
	}
}
