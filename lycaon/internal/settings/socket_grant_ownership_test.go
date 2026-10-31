//go:build linux || darwin

package settings

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func socketStoreFixture(t *testing.T) (*ApprovalStore, func(string) (ApprovalGrant, *socketref.Lease)) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sg-") //nolint:usetesting // Native socket aliases have bounded addresses.
	testutil.FailErr(t, "socket store fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	store, err := NewApprovalStoreAt(filepath.Join(root, "approvals.yaml"))
	testutil.FailErr(t, "socket store", err)
	t.Cleanup(store.Close)
	return store, func(name string) (ApprovalGrant, *socketref.Lease) {
		path := filepath.Join(root, name)
		listener, err := net.Listen("unix", path)
		testutil.FailErr(t, "socket listener", err)
		t.Cleanup(func() { _ = listener.Close() })
		target, err := confine.ResolveSocketRequest(path)
		testutil.FailErr(t, "socket target", err)
		owner, err := socketref.Capture(target.ResolvedPath)
		testutil.FailErr(t, "socket capture", err)
		t.Cleanup(owner.Close)
		return ApprovalGrant{ID: name + owner.Binding().ID, Scope: hitl.ApprovalGrantScopeDevice, Category: ApprovalCategorySocketPath, Pattern: path, Title: "Allow local service", Coverage: path, ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath, Binding: owner.Binding(), GrantedByPersonID: testutil.HostOwner().ID, GrantedAt: time.Now().UTC(), ExpiresWhen: "revoked", ReaskWhen: "service changes"}, owner
	}
}

func assertSocketBindingReleased(t *testing.T, binding socketref.Binding) {
	t.Helper()
	lease, err := binding.Acquire()
	if lease != nil {
		lease.Close()
	}
	if !errors.Is(err, socketref.ErrUnbound) {
		t.Fatalf("binding retained after owner boundary: %v", err)
	}
}

func TestSocketStoreReleasesOwnersWithoutDiscardingRestartIntent(t *testing.T) {
	for _, end := range []string{"close", "expiry", "revoke"} {
		t.Run(end, func(t *testing.T) {
			store, capture := socketStoreFixture(t)
			grant, owner := capture("service")
			expires := time.Now().Add(time.Hour)
			grant.ExpiresAt = &expires
			_, err := store.UpsertGlobalGrant(grant)
			testutil.FailErr(t, "install durable socket", err)
			owner.Close()
			if len(store.SocketPathsForProject("project")) != 1 {
				t.Fatal("installed reference unavailable")
			}
			reopened, err := NewApprovalStoreAt(store.globalPath)
			testutil.FailErr(t, "reload saved socket intent", err)
			defer reopened.Close()
			if rows := reopened.GlobalGrants(); len(rows) != 1 || !rows[0].Unavailable || len(reopened.SocketPathsForProject("project")) != 0 {
				t.Fatal("loading a saved ID recaptured live authority")
			}
			switch end {
			case "close":
				store.Close()
			case "expiry":
				store.expireSocketOwners(expires)
			case "revoke":
				_, err = store.RevokeGlobalGrant(grant.ID)
				testutil.FailErr(t, "revoke socket", err)
			}
			assertSocketBindingReleased(t, grant.Binding)
			if rows := reopened.GlobalGrants(); len(rows) != 1 || rows[0].Binding != grant.Binding || !rows[0].Unavailable {
				t.Fatal("restart lost unavailable intent")
			}
		})
	}
}

func TestSocketStoreFailedWriteKeepsOldOwnerAndReleasesNewOwner(t *testing.T) {
	store, capture := socketStoreFixture(t)
	old, oldOwner := capture("old")
	_, err := store.UpsertGlobalGrant(old)
	testutil.FailErr(t, "install original durable socket", err)
	oldOwner.Close()
	originalPath := store.globalPath
	before, err := os.ReadFile(originalPath)
	testutil.FailErr(t, "read saved intent", err)
	incoming, incomingOwner := capture("incoming")
	store.globalPath = filepath.Dir(originalPath)
	_, err = store.UpsertGlobalGrant(incoming)
	store.globalPath = originalPath
	if err == nil {
		t.Fatal("write over directory succeeded")
	}
	incomingOwner.Close()
	assertSocketBindingReleased(t, incoming.Binding)
	if rows := store.SocketPathsForProject("project"); len(rows) != 1 || rows[0].Binding != old.Binding {
		t.Fatal("failed transaction displaced original owner")
	}
	after, err := os.ReadFile(originalPath)
	testutil.FailErr(t, "read preserved intent", err)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed saved data")
	}
	store.Close()
	assertSocketBindingReleased(t, old.Binding)
}

func TestSocketStoreUnavailableRealizationPreservesIntentWithoutAuthority(t *testing.T) {
	store, capture := socketStoreFixture(t)
	grant, owner := capture("unavailable")
	owner.Close()
	grant.Binding = socketref.Binding{Enforcement: socketref.NoRealization}
	testutil.FailErr(t, "preserve unavailable intent", store.PutGlobal(ApprovalConfig{Grants: []ApprovalGrant{grant}}))
	restored, err := NewApprovalStoreAt(store.globalPath)
	testutil.FailErr(t, "restore unavailable realization", err)
	defer restored.Close()
	rows := restored.GlobalGrants()
	if len(rows) != 1 || !rows[0].Unavailable || rows[0].Binding != grant.Binding || len(restored.SocketPathsForProject("project")) != 0 {
		t.Fatal("unavailable realization became pathname authority or lost intent")
	}
}
