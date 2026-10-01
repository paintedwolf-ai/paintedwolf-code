package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGrantCoversRequiresAnActualLease(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(ApprovalConfig{Posture: gate.PostureStrict}))
	approvalGate := NewRuleApprovalGate(store, NoSources())

	action := hitl.ProposedAction{
		Tool:      "network",
		ProjectID: "proj-1",
		Args:      map[string]any{"host": "example.org", "transport": "http-connect"},
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy},
	}
	if approvalGate.GrantCovers(action) {
		t.Fatal("no host grant must not count as coverage")
	}

	grant := ApprovalGrant{
		ID:    hitl.ApprovalGrantID(hitl.ApprovalGrantScopeDevice, string(ApprovalCategoryHost), "example.org", "", "proj-1", hitl.BoundaryWitness(action.Contained), nil),
		Scope: hitl.ApprovalGrantScopeDevice, Category: ApprovalCategoryHost, Pattern: "example.org",
		ProjectID: "proj-1", Title: "Allow bounded action", Coverage: "example.org",
		Witness:           hitl.BoundaryWitness(action.Contained),
		GrantedByPersonID: testutil.HostOwner().ID,
	}
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)
	if !approvalGate.GrantCovers(action) {
		t.Fatal("matching host grant must cover the destination")
	}
	other := action
	other.Args = map[string]any{"host": "other.example", "transport": "http-connect"}
	if approvalGate.GrantCovers(other) {
		t.Fatal("a grant for example.org must not cover a different host")
	}

	withSocket := action
	withSocket.Contained.SocketPathsDigest = "sock-a"
	withSocket.Contained.SocketCount = 1
	if !approvalGate.GrantCovers(withSocket) {
		t.Fatal("host lease missed after a socket overlay")
	}
}

func TestDeviceWriteRootLeaseCoversOtherProject(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal balanced", store.PutGlobal(ApprovalConfig{Posture: gate.PostureBalanced}))
	approvalGate := NewRuleApprovalGate(store, NoSources())

	first := hitl.ProposedAction{
		Tool: "write_root", SessionID: "chat-a", ProjectID: "proj-a", ProjectDir: "/tmp/a",
		Args:      map[string]any{"proposed_write_root": "/Users/me/go"},
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/a"}},
	}
	offers := approvalGate.GrantOffers(first, &hitl.ApprovalResult{Decision: askDecision(api.GateOutsideRootsWrite)})
	var device *hitl.ApprovalGrantOffer
	for _, offer := range offers {
		if offer.Grant.Predicate.Category == string(ApprovalCategoryWriteRoot) &&
			offer.Scope == hitl.ApprovalGrantScopeDevice {
			copy := offer
			device = &copy
		}
	}
	if device == nil {
		t.Fatal("write-root card omitted the device rung")
	}
	if strings.Contains(device.Coverage, hitl.DeviceCoverageSuffix) {
		t.Fatalf("write-root device coverage is cross-project, got %q", device.Coverage)
	}
	device.Grant.GrantedByPersonID = testutil.HostOwner().ID
	_, err = approvalGate.ApplyGrant(device.Grant)
	testutil.FailErr(t, "apply device write-root grant", err)

	other := hitl.ProposedAction{
		Tool: "write_root", SessionID: "chat-b", ProjectID: "proj-b", ProjectDir: "/tmp/b",
		Args:      map[string]any{"proposed_write_root": "/Users/me/go"},
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/b"}},
	}
	if !approvalGate.GrantCovers(other) {
		t.Fatal("device write-root lease must cover the same path on another project")
	}
	if got := store.WriteRootsForProject("proj-b"); len(got) != 1 || got[0] != "/Users/me/go" {
		t.Fatalf("device write-root missing from other project profile: %v", got)
	}

	toolAsk := hitl.ProposedAction{
		Tool: "chown", SessionID: "chat-b", ProjectID: "proj-b", ProjectDir: "/tmp/b",
		Args:      map[string]any{"path": "/Users/me/go/bin/tea"},
		Contained: other.Contained,
	}
	if approvalGate.GrantCovers(toolAsk) {
		t.Fatal("a write-root lease must not satisfy a tool ask")
	}
}

func TestPackageCoordinateLeaseCoversFlagAndEnvVariants(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal balanced", store.PutGlobal(ApprovalConfig{Posture: gate.PostureBalanced}))
	approvalGate := NewRuleApprovalGate(store, NoSources())

	pkgExecBase := &packageexec.Execution{
		Manager:   "npm",
		Operation: packageexec.OperationDependencyInstall,
		Packages: []packageexec.Package{
			{System: "npm", Name: "lodash", RequestedVersion: "4.17.21", Status: packageexec.IdentityResolved},
		},
	}
	pattern := hitl.PackageCoordinatePattern(pkgExecBase)
	grant := hitl.ApprovalGrant{
		ID:            "grant-pkg",
		Scope:         hitl.ApprovalGrantScopeChat,
		ChatSessionID: "chat-task-1",
		ProjectID:     "proj-1",
		Witness:       hitl.BoundaryWitness(hitl.Contained{}),
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryPackageCoordinate, Pattern: pattern},
	}
	_, err = approvalGate.ApplyGrant(grant)
	testutil.FailErr(t, "apply package coordinate grant", err)

	action1 := hitl.ProposedAction{
		Tool:             "command",
		SessionID:        "chat-task-1",
		ProjectID:        "proj-1",
		Command:          "npm install lodash@4.17.21",
		PackageExecution: pkgExecBase,
	}
	if !approvalGate.GrantCovers(action1) {
		t.Fatal("package coordinate grant must cover base command")
	}

	pkgExecFlagVariant := &packageexec.Execution{
		Manager:   "npm",
		Operation: packageexec.OperationDependencyInstall,
		Packages: []packageexec.Package{
			{System: "npm", Name: "lodash", RequestedVersion: "4.17.21", Status: packageexec.IdentityResolved},
		},
	}
	action2 := hitl.ProposedAction{
		Tool:             "command",
		SessionID:        "chat-task-1",
		ProjectID:        "proj-1",
		Command:          "npm install lodash@4.17.21 --save-dev --verbose",
		PackageExecution: pkgExecFlagVariant,
	}
	if !approvalGate.GrantCovers(action2) {
		t.Fatal("package coordinate grant must cover command with flag variants")
	}

	action3 := hitl.ProposedAction{
		Tool:             "command",
		SessionID:        "chat-task-1",
		ProjectID:        "proj-1",
		Command:          "NODE_ENV=production npm install lodash@4.17.21",
		PackageExecution: pkgExecBase,
	}
	if !approvalGate.GrantCovers(action3) {
		t.Fatal("package coordinate grant must cover command with env variants")
	}
}

