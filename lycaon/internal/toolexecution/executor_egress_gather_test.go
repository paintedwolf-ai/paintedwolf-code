package toolexecution_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
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

// Two hosts one command reaches inside the settle window are one card over the
// observed set, and both dials are released by the one answer.
func TestConcurrentHostsFromOneCommandAreOneSetCard(t *testing.T) {
	exec, mgr := gatherExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := confine.EgressCommand{SessionID: "chat-1", RootSessionID: "chat-1", ToolCallID: "tc-pytest", CommandLine: "pytest -n auto", ProjectID: "proj", ProjectDir: "/tmp/proj"}

	results := make(chan bool, 2)
	for _, host := range []string{"api.osv.dev", "attack.mitre.org"} {
		go func(host string) { results <- exec.ResolveEgressForTest(ctx, cmd, host) }(host)
	}
	for i := 0; i < 2; i++ {
		select {
		case ok := <-results:
			if !ok {
				t.Fatalf("dial %d denied", i)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for the set card")
		}
	}
	reqs := mgr.requests()
	if len(reqs) != 1 {
		t.Fatalf("checkpoint requests = %d, want one set card: %+v", len(reqs), reqs)
	}
	req := reqs[0]
	if req.DeclaredEndpoints == nil || req.DeclaredEndpoints.HostCount != 2 || req.DeclaredEndpoints.Source != toolexecution.DeclaredEndpointObserved {
		t.Fatalf("set card endpoints = %+v, want two observed hosts", req.DeclaredEndpoints)
	}
	plan, err := hitl.CompileCheckpointApprovalPlan(req)
	if err != nil {
		t.Fatalf("compile set plan: %v", err)
	}
	if plan.Subject.Kind != hitl.ApprovalSubjectDestinationSet || len(plan.Subject.Targets) != 2 {
		t.Fatalf("set subject = %+v", plan.Subject)
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
	// Past the window: the next host is a second card for the same command.
	time.Sleep(700 * time.Millisecond)
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
