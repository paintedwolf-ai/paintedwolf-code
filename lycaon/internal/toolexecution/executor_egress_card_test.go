package toolexecution_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
)

// gatherHITL records every checkpoint request so a test can read the plan the
// executor compiled, and approves them all.
type gatherHITL struct {
	coalesceHITL
	reqs  []hitl.CheckpointRequest
	reqMu sync.Mutex
}

func (m *gatherHITL) RequestCheckpoint(ctx context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.reqMu.Lock()
	m.reqs = append(m.reqs, req)
	m.reqMu.Unlock()
	return m.coalesceHITL.RequestCheckpoint(ctx, req)
}

func (m *gatherHITL) requests() []hitl.CheckpointRequest {
	m.reqMu.Lock()
	defer m.reqMu.Unlock()
	return append([]hitl.CheckpointRequest(nil), m.reqs...)
}

func gatherExecutor(t *testing.T) (*toolexecution.Executor, *gatherHITL) {
	t.Helper()
	mgr := &gatherHITL{coalesceHITL: coalesceHITL{req: make(chan struct{}, 8)}}
	mgr.approve()
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	exec.Approvals.SetToolApprovalCoalesce(coalesceAdapter{rt: approvalstate.NewToolApprovalCoalesce()})
	// The ingestion producer reports "read untrusted content": at Balanced that
	// is what turns new-host cards on.
	exec.Secrets.SetUntrustedIngestionSource(func(context.Context, string) (bool, error) { return true, nil })
	return exec, mgr
}

// Each recipient is its own decision: two hosts one command reaches at once
// raise one card each, and each card names its host alone.
func TestConcurrentHostsFromOneCommandAskSeparately(t *testing.T) {
	exec, mgr := gatherExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
	defer cancel()
	cmd := confine.EgressCommand{SessionID: "chat-1", RootSessionID: "chat-1", ToolCallID: "tc-pytest", CommandLine: "pytest -n auto", ProjectID: "proj", ProjectDir: "/tmp/proj"}

	hosts := []string{"api.osv.dev", "attack.mitre.org"}
	results := make(chan bool, len(hosts))
	for _, host := range hosts {
		go func(host string) { results <- exec.ResolveEgressForTest(ctx, cmd, host) }(host)
	}
	for i := range hosts {
		select {
		case ok := <-results:
			if !ok {
				t.Fatalf("dial %d denied", i)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for the cards")
		}
	}
	reqs := mgr.requests()
	if len(reqs) != len(hosts) {
		t.Fatalf("checkpoint requests = %d, want one per host: %+v", len(reqs), reqs)
	}
	named := map[string]bool{}
	for _, req := range reqs {
		if req.DeclaredEndpoints != nil {
			t.Fatalf("an observed host was folded into a set: %+v", req.DeclaredEndpoints)
		}
		plan, err := hitl.CompileCheckpointApprovalPlan(req)
		testutil.FailErr(t, "compile plan", err)
		if plan.Subject.Kind == hitl.ApprovalSubjectDestinationSet {
			t.Fatalf("subject = %+v, want one destination", plan.Subject)
		}
		var host string
		if req.ProposedAction != nil {
			host, _ = req.ProposedAction.Invocation.Args["host"].(string)
		}
		named[host] = true
	}
	for _, host := range hosts {
		if !named[host] {
			t.Fatalf("no card named %s: %v", host, named)
		}
	}
}

// Repeated hosts keep the same scope on the primary chat option.
func TestRepeatedHostsKeepBalancedTaskScope(t *testing.T) {
	exec, mgr := gatherExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := confine.EgressCommand{SessionID: "chat-2", RootSessionID: "chat-2", ToolCallID: "tc-poe", CommandLine: "poe test-integration", ProjectID: "proj", ProjectDir: "/tmp/proj"}

	if !exec.ResolveEgressForTest(ctx, cmd, "api.github.com") {
		t.Fatal("first host denied")
	}
	if !exec.ResolveEgressForTest(ctx, cmd, "cve.circl.lu") {
		t.Fatal("second host denied")
	}
	reqs := mgr.requests()
	if len(reqs) != 2 {
		t.Fatalf("checkpoint requests = %d, want two: %+v", len(reqs), reqs)
	}
	taskSlot := func(offers []hitl.ApprovalGrantOffer) hitl.ApprovalGrantOffer {
		for _, offer := range offers {
			if offer.Rung == hitl.ApprovalRungChat && offer.Group == "" {
				return offer
			}
		}
		return hitl.ApprovalGrantOffer{}
	}
	for _, req := range reqs {
		if task := taskSlot(req.GrantOffers); task.Grant.Predicate.Category == hitl.ApprovalGrantCategoryEgressCommand {
			t.Fatal("Balanced widened the primary subject")
		}
		found := false
		for _, offer := range req.GrantOffers {
			if offer.Grant.Predicate.Category == hitl.ApprovalGrantCategoryEgressCommand && offer.Group == hitl.GroupAlsoAllow {
				found = true
			}
		}
		if !found {
			t.Fatal("explicit wider choice disappeared")
		}
	}
}

func TestCheckpointOwnerReleaseKeepsCurrentBrokerApproval(t *testing.T) {
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
	old, oldManager := gatherExecutor(t)
	current, currentManager := gatherExecutor(t)
	// Parsed HTTP is ingestion at Balanced; Strict reviews the first host.
	current.Network.SetEgressPostureSource(func(string) gate.Posture { return gate.PostureStrict })
	t.Cleanup(func() {
		if err := current.Approvals.ReleaseEgressResolver(context.Background()); err != nil {
			t.Errorf("release current checkpoint owner: %v", err)
		}
	})
	if err := old.Approvals.ReleaseEgressResolver(t.Context()); err != nil {
		t.Fatalf("release old checkpoint owner: %v", err)
	}
	cmd := confine.EgressCommand{SessionID: "current-owner", RootSessionID: "current-owner", ToolCallID: "current-dial"}
	if !confine.DecideAttributedHost(t.Context(), cmd, "owner-approval.test") {
		t.Fatal("current checkpoint owner did not approve its broker dial")
	}
	if len(oldManager.requests()) != 0 || len(currentManager.requests()) != 1 {
		t.Fatalf("broker reached wrong checkpoint owner: old=%d current=%d", len(oldManager.requests()), len(currentManager.requests()))
	}
	if err := current.Approvals.ReleaseEgressResolver(t.Context()); err != nil {
		t.Fatalf("release current checkpoint owner: %v", err)
	}
	cmd.ToolCallID = "closed-dial"
	if confine.DecideAttributedHost(t.Context(), cmd, "closed-owner.test") {
		t.Fatal("closed checkpoint owner authorized a new broker dial")
	}
	if len(currentManager.requests()) != 1 {
		t.Fatal("closed owner minted an additional checkpoint")
	}
}

// Light offers the command's network as a primary choice beside the host's
// chat permission, never in its place: the face stays the host, so taking it
// at Light never leaves more to ask than taking it at Balanced.
func TestLightOffersCommandNetworkBesideTheHost(t *testing.T) {
	exec, mgr := gatherExecutor(t)
	exec.Network.SetEgressPostureSource(func(string) gate.Posture { return gate.PostureLight })
	ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
	defer cancel()
	cmd := confine.EgressCommand{
		SessionID: "chat-light", RootSessionID: "chat-light", ToolCallID: "tc-light", CommandLine: "curl https://api.example.test/a",
		ProjectID: "proj", ProjectDir: "/tmp/proj",
		UserRule: &confine.EgressUserRule{Category: "host", Pattern: "api.example.test", Subject: "api.example.test"},
	}
	if !exec.ResolveEgressForTest(ctx, cmd, "api.example.test") {
		t.Fatal("dial denied")
	}
	reqs := mgr.requests()
	if len(reqs) != 1 {
		t.Fatalf("checkpoint requests = %d, want 1", len(reqs))
	}
	plan, err := hitl.CompileCheckpointApprovalPlan(reqs[0])
	testutil.FailErr(t, "compile plan", err)
	face, ok := plan.Option(plan.RecommendedOptionID)
	if !ok || face.Title == hitl.TitleAllowCommandNetworkForThisChat {
		t.Fatalf("Light face = %+v, want the host's chat permission", face)
	}
	for _, option := range plan.Options {
		if option.Title == hitl.TitleAllowCommandNetworkForThisChat && option.Group == "" && !option.Disabled {
			return
		}
	}
	t.Fatalf("Light card lacks the command network as a primary choice: %+v", plan.Options)
}
