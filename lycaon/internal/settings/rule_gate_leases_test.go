package settings_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRuleApprovalGateExactActionSetLease(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk,
	}})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := filepath.Join(tmp, "project")

	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "git push origin main"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}},
		},
	}

	first, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate first action", err)
	if !first.Required() {
		t.Fatalf("first irreversible action must ask: %+v", first)
	}
	offer := hitl.ExactActionSetOffer(action, []string{hitl.GrantKey(action)})
	created, err := approvalGate.ApplyGrant(offer.Grant)
	testutil.FailErr(t, "approvalGate.ApplyGrant", err)
	if !created {
		t.Fatal("exact action-set lease was not created")
	}

	repeat, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate repeat action", err)
	if !repeat.AutoApproved() || repeat.Required() {
		t.Fatalf("exact covered action must auto-approve: %+v", repeat)
	}

	other, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "git push origin release"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}},
		},
	})
	testutil.FailErr(t, "gate.Evaluate other escape", err)
	if !other.Required() {
		t.Fatalf("a different command must still ask: %+v", other)
	}

	otherChat := action
	otherChat.Scope.SessionID = "chat-2"
	cross, err := approvalGate.Evaluate(context.Background(), otherChat)
	testutil.FailErr(t, "gate.Evaluate other chat", err)
	if !cross.Required() {
		t.Fatalf("same command in a different chat must still ask: %+v", cross)
	}

	approvalGate.ForgetSession("chat-1")
	after, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate after forget", err)
	if !after.Required() {
		t.Fatalf("after ForgetSession the repeat must ask again: %+v", after)
	}
}

func TestRemotePackageExecutionApprovalBindsResolvedVersion(t *testing.T) {
	stageBundledApprovalsPosture(t, gate.PostureLight)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	project := t.TempDir()
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "npx create-app@latest demo"},
		},
		Scope: hitl.ActionScope{
			ProjectID:  "project",
			ProjectDir: project,
			SessionID:  "chat",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{project}},
			PackageExecution: &packageexec.Execution{
				Manager: "npm", Operation: packageexec.OperationRemoteExecute,
				Packages: []packageexec.Package{{System: "NPM", Name: "create-app", RequestedVersion: "latest", ResolvedVersion: "1.2.3", Status: packageexec.IdentityResolved}},
			},
		},
	}
	first, err := approvalGate.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate first package action", err)
	if !first.Required() || first.Decision == nil || first.Decision.Primary != api.GateRemotePackageExecution {
		t.Fatalf("first package action = %+v", first)
	}
	offer := hitl.ExactActionOffer(action)
	_, err = approvalGate.ApplyGrant(offer.Grant)
	testutil.FailErr(t, "apply exact package grant", err)
	repeat, err := approvalGate.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate repeated package action", err)
	if !repeat.AutoApproved() {
		t.Fatalf("same resolved package action should reuse approval: %+v", repeat)
	}
	changed := action
	changed.Execution.PackageExecution = &packageexec.Execution{
		Manager: "npm", Operation: packageexec.OperationRemoteExecute,
		Packages: []packageexec.Package{{System: "NPM", Name: "create-app", RequestedVersion: "latest", ResolvedVersion: "1.2.4", Status: packageexec.IdentityResolved}},
	}
	next, err := approvalGate.Evaluate(t.Context(), changed)
	testutil.FailErr(t, "evaluate changed package version", err)
	if !next.Required() {
		t.Fatalf("changed resolved version reused prior approval: %+v", next)
	}
}

func TestGateRemotePackageExecutionDurableProjectGrant(t *testing.T) {
	approvalsPath := filepath.Join(t.TempDir(), "approvals.yaml")
	store, err := settings.NewApprovalStoreAt(approvalsPath)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	project := t.TempDir()
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "npx prisma generate"},
		},
		Scope: hitl.ActionScope{
			ProjectID:  "proj-123",
			ProjectDir: project,
			SessionID:  "session-1",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{project}},
			PackageExecution: &packageexec.Execution{
				Manager: "npm", Operation: packageexec.OperationRemoteExecute,
				Packages: []packageexec.Package{{System: "NPM", Name: "prisma", RequestedVersion: "8.0.0-rc.15", ResolvedVersion: "8.0.0-rc.15", Status: packageexec.IdentityResolved}},
			},
		},
	}
	check, err := approvalGate.Evaluate(t.Context(), action)
	testutil.FailErr(t, "check package action", err)
	if !check.Required() {
		t.Fatalf("expected approval required, got %+v", check)
	}
	offers := approvalGate.GrantOffers(action, check)
	var projectGrant *hitl.ApprovalGrant
	for _, offer := range offers {
		if offer.Grant.Predicate.Category == hitl.ApprovalGrantCategoryPackageCoordinate && offer.Grant.Scope == hitl.ApprovalGrantScopeProject {
			g := offer.Grant
			projectGrant = &g
			break
		}
	}
	if projectGrant == nil {
		t.Fatalf("expected project-scoped package_coordinate offer in: %+v", offers)
	}

	projectGrant.GrantedByPersonID = testutil.HostOwner().ID
	created, err := approvalGate.ApplyGrant(*projectGrant)
	testutil.FailErr(t, "ApplyGrant for project-scoped package_coordinate", err)
	if !created {
		t.Fatal("expected durable project grant to be created")
	}

	repeat, err := approvalGate.Evaluate(t.Context(), action)
	testutil.FailErr(t, "check repeated package action", err)
	if !repeat.AutoApproved() {
		t.Fatalf("repeated check verdict = %+v, want AutoApproved", repeat)
	}

	reloadedStore, err := settings.NewApprovalStoreAt(approvalsPath)
	testutil.FailErr(t, "reloaded NewApprovalStoreAt", err)
	reloadedGate := settings.NewRuleApprovalGate(reloadedStore, settings.NoSources())
	afterReload, err := reloadedGate.Evaluate(t.Context(), action)
	testutil.FailErr(t, "check package action after reload", err)
	if !afterReload.AutoApproved() {
		t.Fatalf("after reload check verdict = %+v, want AutoApproved", afterReload)
	}
}

// Durable leases retain their exact action set across reload.
func TestDurableExactActionLeaseRoundTrips(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk,
	}})
	path := filepath.Join(tmp, "global.yaml")
	store, err := settings.NewApprovalStoreAt(path)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := filepath.Join(tmp, "project")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "aws s3 rb s3://bucket --force"},
		},
		Scope: hitl.ActionScope{
			ProjectID:  "proj-durable",
			ProjectDir: proj,
			SessionID:  "chat-durable",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}},
		},
	}
	offer := hitl.ExactActionOffer(action)
	grant := offer.Grant
	grant.Scope = hitl.ApprovalGrantScopeProject
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	grant.ExpiresAt = &expires
	grant.ExpiresWhen = hitl.ExpiresIn7DaysOrRevoked
	grant.Title = hitl.TitleAllowForThisProject
	grant.ChatSessionID = ""
	grant.ID = hitl.ApprovalGrantID(
		grant.Scope, grant.Predicate.Category, grant.Predicate.Pattern,
		grant.ChatSessionID, grant.ProjectID, grant.Witness, grant.ExactActionSet,
	)
	grant.GrantedByPersonID = testutil.HostOwner().ID
	created, err := approvalGate.ApplyGrant(grant)
	testutil.FailErr(t, "approvalGate.ApplyGrant", err)
	if !created {
		t.Fatal("project exact-action lease was not created")
	}

	reloaded, err := settings.NewApprovalStoreAt(path)
	testutil.FailErr(t, "reload approval store", err)
	gate2 := settings.NewRuleApprovalGate(reloaded, settings.NoSources())
	repeat, err := gate2.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate after reload", err)
	if !repeat.AutoApproved() || repeat.Required() {
		t.Fatalf("durable exact-action lease must cover after reload: %+v", repeat)
	}
	other := action
	other.Invocation.Args = map[string]any{"command": "aws s3 rb s3://other --force"}
	cross, err := gate2.Evaluate(context.Background(), other)
	testutil.FailErr(t, "gate.Evaluate near-identical", err)
	if !cross.Required() {
		t.Fatalf("a different exact action must still ask: %+v", cross)
	}
}

// Confinement failures remain active when an action is leased.
func TestRuleApprovalGateHostResourceAskUsesOneApprovalAndExactLease(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsYAML(t, `approval_posture: balanced
rules:
  - category: tool
    pattern: chown
    effect: ask
  - category: host_resource
    pattern: local-db
    effect: ask
`)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "new approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "chown",
		},
		Scope: hitl.ActionScope{
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
			ProjectDir: "/tmp/proj",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"local-db"},
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{tmp}},
		},
	}

	first, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate first", err)
	if !first.Required() || !first.HostResourceApproval {
		t.Fatalf("tool and host-resource asks should share one approval result: %+v", first)
	}
	offers := approvalGate.GrantOffers(action, first)
	var ordinary, hostResource *hitl.ApprovalGrantOffer
	for i := range offers {
		switch offers[i].Grant.Predicate.Category {
		case string(settings.ApprovalCategoryTool):
			ordinary = &offers[i]
		case string(settings.ApprovalCategoryHostResource):
			hostResource = &offers[i]
		}
	}
	if ordinary == nil || hostResource == nil {
		t.Fatalf("one card should offer independently scoped tool and host-resource leases: %+v", offers)
	}

	ordinary.Grant.GrantedByPersonID = testutil.HostOwner().ID
	_, err = approvalGate.ApplyGrant(ordinary.Grant)
	testutil.FailErr(t, "apply ordinary grant", err)
	second, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate after ordinary grant", err)
	if !second.Required() || !second.HostResourceApproval {
		t.Fatalf("ordinary grant must not suppress host-resource consent: %+v", second)
	}

	hostResource.Grant.GrantedByPersonID = testutil.HostOwner().ID
	_, err = approvalGate.ApplyGrant(hostResource.Grant)
	testutil.FailErr(t, "apply host-resource grant", err)
	final, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate after both grants", err)
	if !final.AutoApproved() || final.Required() {
		t.Fatalf("exact leases should suppress the repeated alert: %+v", final)
	}
}

func TestRuleApprovalGateExplicitHostResourceAskAppliesAtLight(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsYAML(t, `approval_posture: light
rules:
  - category: host_resource
    pattern: local-db
    effect: ask
`)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "new approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	result, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "read",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"local-db"},
		},
	})
	testutil.FailErr(t, "evaluate", err)
	if !result.Required() || !result.HostResourceApproval {
		t.Fatalf("an explicit ask is an opt-in alert even at Light: %+v", result)
	}
}

func TestRuleApprovalGateCapabilityLeasesComposeWithoutWidening(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsYAML(t, `rules:
  - {category: host_resource, pattern: docker, effect: ask}
  - {category: host_resource, pattern: local-db, effect: ask}
  - {category: host_resource, pattern: debug-tools, effect: ask}
`)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "new approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{tmp}}
	for _, id := range []string{"docker", "local-db"} {
		action := hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "read",
			},
			Scope: hitl.ActionScope{
				SessionID: "chat-1",
			},
			Resources: hitl.ActionResources{
				HostResources: []string{id},
			},
			Execution: hitl.ActionExecution{
				Contained: contained,
			},
		}
		result, evalErr := approvalGate.Evaluate(context.Background(), action)
		testutil.FailErr(t, "evaluate "+id, evalErr)
		offers := approvalGate.GrantOffers(action, result)
		if len(offers) == 0 || offers[0].Grant.Predicate.Category != string(settings.ApprovalCategoryHostResource) {
			t.Fatalf("capability offer for %s = %+v", id, offers)
		}
		_, applyErr := approvalGate.ApplyGrant(offers[0].Grant)
		testutil.FailErr(t, "apply "+id, applyErr)
	}

	covered, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "read",
		},
		Scope: hitl.ActionScope{
			SessionID: "chat-1",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"local-db", "docker"},
		},
		Execution: hitl.ActionExecution{
			Contained: contained,
		},
	})
	testutil.FailErr(t, "evaluate composed coverage", err)
	if !covered.AutoApproved() || covered.Required() {
		t.Fatalf("independent exact leases should compose: %+v", covered)
	}
	uncovered, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "read",
		},
		Scope: hitl.ActionScope{
			SessionID: "chat-1",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"docker", "local-db", "debug-tools"},
		},
		Execution: hitl.ActionExecution{
			Contained: contained,
		},
	})
	testutil.FailErr(t, "evaluate uncovered id", err)
	if !uncovered.Required() || !uncovered.HostResourceApproval {
		t.Fatalf("an unapproved id must still ask: %+v", uncovered)
	}
}

func TestRuleApprovalGateCapabilityDenySurvivesNeverAsk(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsYAML(t, `never_ask: true
rules:
  - category: host_resource
    pattern: docker
    effect: deny
  - category: tool
    pattern: edit
    effect: deny
`)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "new approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	result, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"docker"},
		},
	})
	testutil.FailErr(t, "evaluate", err)
	if !result.Denied || result.AutoApproved() {
		t.Fatalf("capability deny is enforcement, not an ask never_ask can clear: %+v", result)
	}
	result, err = approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "edit",
		},
	})
	testutil.FailErr(t, "evaluate tool deny", err)
	if !result.Denied || result.AutoApproved() {
		t.Fatalf("tool deny is enforcement, not an ask never_ask can clear: %+v", result)
	}
}

func TestHostResourceDeviceLeaseCoversOtherProject(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsYAML(t, `rules:
  - {category: host_resource, pattern: docker, effect: ask}
`)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "new approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	first := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
		},
		Scope: hitl.ActionScope{
			SessionID:  "chat-a",
			ProjectID:  "proj-a",
			ProjectDir: "/tmp/a",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"docker"},
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/a"}},
		},
	}
	result, err := approvalGate.Evaluate(context.Background(), first)
	testutil.FailErr(t, "evaluate first project", err)
	if !result.Required() || !result.HostResourceApproval {
		t.Fatalf("first project should ask for docker: %+v", result)
	}
	var device *hitl.ApprovalGrantOffer
	for _, offer := range approvalGate.GrantOffers(first, result) {
		if offer.Grant.Predicate.Category == string(settings.ApprovalCategoryHostResource) &&
			offer.Scope == hitl.ApprovalGrantScopeDevice {
			copy := offer
			device = &copy
		}
	}
	if device == nil {
		t.Fatal("host-resource card omitted the device rung")
	}
	device.Grant.GrantedByPersonID = testutil.HostOwner().ID
	_, err = approvalGate.ApplyGrant(device.Grant)
	testutil.FailErr(t, "apply device host-resource grant", err)

	other := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
		},
		Scope: hitl.ActionScope{
			SessionID:  "chat-b",
			ProjectID:  "proj-b",
			ProjectDir: "/tmp/b",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"docker"},
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{"/tmp/b"}},
		},
	}
	if !approvalGate.HostResourceLeaseCovers(other) {
		t.Fatal("device host-resource lease must cover the same catalog id on another project")
	}
	covered, err := approvalGate.Evaluate(context.Background(), other)
	testutil.FailErr(t, "evaluate other project", err)
	if covered.HostResourceApproval {
		t.Fatalf("device lease left a host-resource ask pending: %+v", covered)
	}
}

func TestHostResourceLeaseDoesNotSatisfyToolAsk(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsYAML(t, `rules:
  - {category: tool, pattern: chown, effect: ask}
  - {category: host_resource, pattern: docker, effect: ask}
`)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "new approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "chown",
		},
		Scope: hitl.ActionScope{
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
			ProjectDir: "/tmp/proj",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"docker"},
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{tmp}},
		},
	}
	first, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate", err)
	var hostResource *hitl.ApprovalGrantOffer
	for _, offer := range approvalGate.GrantOffers(action, first) {
		if offer.Grant.Predicate.Category == string(settings.ApprovalCategoryHostResource) &&
			offer.Scope == hitl.ApprovalGrantScopeChat {
			copy := offer
			hostResource = &copy
		}
	}
	if hostResource == nil {
		t.Fatal("missing host-resource task offer")
	}
	_, err = approvalGate.ApplyGrant(hostResource.Grant)
	testutil.FailErr(t, "apply host-resource grant", err)
	second, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate after host-resource grant", err)
	if !second.Required() || second.HostResourceApproval {
		t.Fatalf("host-resource lease must not satisfy the tool ask: %+v", second)
	}
}

// gateGateUnobservedChannel is the channel gate name, spelled once here so these
// tests read the vocabulary rather than restating it.
const gateGateUnobservedChannel = api.GateUnobservedChannel

// Control-plane denials preserve the resolver's sink-code precedence.
