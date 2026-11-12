package toolexecution_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
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
	ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
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

func TestSiblingProgramChildrenKeepSeparateObservedEgressReviews(t *testing.T) {
	executor, manager := gatherExecutor(t)
	ctx, cancel := context.WithTimeout(t.Context(), testutil.Timeout(time.Minute))
	defer cancel()
	results := make(chan bool, 2)
	for i, child := range []string{"first-child", "second-child"} {
		cmd := confine.EgressCommand{SessionID: "child-session", RootSessionID: "child-session", ToolCallID: "actual-model-call", ActionID: child, ProjectID: "project", ProjectDir: "/fixture/project"}
		host := []string{"first.invalid", "second.invalid"}[i]
		go func() { results <- executor.ResolveEgressForTest(ctx, cmd, host) }()
	}
	for range 2 {
		select {
		case allowed := <-results:
			if !allowed {
				t.Fatal("fixture approval did not authorize its action")
			}
		case <-ctx.Done():
			t.Fatal("child review did not settle")
		}
	}
	if requests := manager.requests(); len(requests) != 2 {
		t.Fatalf("sibling actions combined into one observed-set review: count%d", len(requests))
	}
}

// contactLog records the contacts the executor writes for allowed dials.
type contactLog struct {
	mu    sync.Mutex
	kinds []evidence.Contact
}

func (*contactLog) SessionContacts(string) map[string]evidence.Contact { return nil }

func (l *contactLog) RecordContact(_ context.Context, _, _ string, contact evidence.Contact) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kinds = append(l.kinds, contact)
	return nil
}

// A reader following a displayed link is a quiet retrieval while the chat is
// known to hold no credential. Once it holds one, or exposure is unknown, the
// dial is reviewed and recorded as the chat's own. A source stays a retrieval
// either way.
func TestDisplayedLinkBecomesTheChatsOwnDialOnceACredentialIsHeld(t *testing.T) {
	exposed := func(held bool) func(context.Context, string) (bool, error) {
		return func(context.Context, string) (bool, error) { return held, nil }
	}
	for name, tc := range map[string]struct {
		origin   confine.AddressOrigin
		exposure func(context.Context, string) (bool, error)
		asks     int
		contact  evidence.Contact
	}{
		"displayed link, no credential":     {origin: confine.AddressDisplayed, exposure: exposed(false), contact: evidence.ContactRetrieval},
		"displayed link, credential held":   {origin: confine.AddressDisplayed, exposure: exposed(true), asks: 1, contact: evidence.ContactOwn},
		"displayed link, exposure unknown":  {origin: confine.AddressDisplayed, asks: 1, contact: evidence.ContactOwn},
		"source, credential held":           {origin: confine.AddressSource, exposure: exposed(true), contact: evidence.ContactRetrieval},
		"authored address, no credential":   {origin: confine.AddressAuthored, exposure: exposed(false), asks: 1, contact: evidence.ContactOwn},
		"authored address, credential held": {origin: confine.AddressAuthored, exposure: exposed(true), asks: 1, contact: evidence.ContactOwn},
	} {
		t.Run(name, func(t *testing.T) {
			exec, mgr := gatherExecutor(t)
			contacts := &contactLog{}
			exec.Network.SetSessionHostLedger(contacts)
			if tc.exposure != nil {
				exec.Secrets.SetSecretExposureSource(tc.exposure)
			}
			ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
			defer cancel()
			cmd := confine.EgressCommand{
				SessionID: "chat", RootSessionID: "chat", ToolCallID: "call", ProjectID: "proj", ProjectDir: "/tmp/proj",
				Image: ingestion.ToolFetchURL, Origin: tc.origin,
			}
			if !exec.ResolveEgressForTest(ctx, cmd, "page.example") {
				t.Fatal("the dial was refused")
			}
			if got := len(mgr.requests()); got != tc.asks {
				t.Fatalf("asks = %d, want %d", got, tc.asks)
			}
			if !slices.Equal(contacts.kinds, []evidence.Contact{tc.contact}) {
				t.Fatalf("recorded contacts = %v, want %v", contacts.kinds, tc.contact)
			}
		})
	}
}
