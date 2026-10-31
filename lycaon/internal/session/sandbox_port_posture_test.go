package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	sessionexecution "github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// fixedProvenance states which ports this chat serves and which a listener it
// does not own already holds.
type fixedProvenance struct {
	owned map[uint16]bool
	held  map[uint16]bool
}

func (f fixedProvenance) IsSessionOwned(_ context.Context, _, _ string, port uint16) (bool, ProvenanceEvidence) {
	if f.owned[port] {
		return true, ProvenanceEvidence{Method: ProvenanceLineage}
	}
	return false, ProvenanceEvidence{Method: ProvenanceUnverified}
}

func (f fixedProvenance) HeldByOther(_ context.Context, _, _ string, port uint16) bool {
	return f.held[port]
}

func (f fixedProvenance) RecordSessionContainer(sessionID, containerID, socketPath string) {}
func (f fixedProvenance) ForgetSession(sessionID string)                                   {}

func postureOf(p gate.Posture) func(string) gate.Posture {
	return func(string) gate.Posture { return p }
}

type portBrokers struct {
	listenRT, loopbackRT *approvalstate.SandboxPortGrantRuntime
	listen               *ListenCheckpointBroker
	loopback             *LoopbackCheckpointBroker
	network              *LocalNetworkCheckpointBroker
}

func newPortBrokers(posture gate.Posture, prov LoopbackProvenanceResolver) portBrokers {
	listenRT := approvalstate.NewSandboxPortGrantRuntime()
	loopbackRT := approvalstate.NewSandboxPortGrantRuntime()
	return portBrokers{
		listenRT: listenRT, loopbackRT: loopbackRT,
		listen:   &ListenCheckpointBroker{Runtime: listenRT, Loopback: loopbackRT, Provenance: prov, Posture: postureOf(posture)},
		loopback: &LoopbackCheckpointBroker{Runtime: loopbackRT, Provenance: prov, Posture: postureOf(posture)},
		network:  &LocalNetworkCheckpointBroker{Listen: listenRT, Loopback: loopbackRT, Provenance: prov, Posture: postureOf(posture)},
	}
}

func (b portBrokers) listenOn(t *testing.T, chatID string, ports ...uint16) {
	t.Helper()
	res, err := b.listen.Await(context.Background(), tools.LocalListenAsk{SessionID: chatID, ProjectDir: "/tmp/proj", Ports: ports})
	if err != nil {
		t.Fatalf("listen Await: %v", err)
	}
	if !res.Authorized || res.Raised {
		t.Fatalf("listen on %v: authorized=%v raised=%v, want silent", ports, res.Authorized, res.Raised)
	}
}

func (b portBrokers) connect(t *testing.T, chatID string, ports ...uint16) tools.LoopbackConnectResult {
	t.Helper()
	res, err := b.loopback.Await(context.Background(), tools.LoopbackConnectAsk{SessionID: chatID, ProjectDir: "/tmp/proj", Ports: ports})
	if err != nil {
		t.Fatalf("loopback Await: %v", err)
	}
	return res
}

func TestPortBrokersPostureNuances(t *testing.T) {
	t.Parallel()

	t.Run("LightPostureIsSilentForListenAndLoopback", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureLight, fixedProvenance{})
		b.listenOn(t, "chat-1", 8123)
		if res := b.connect(t, "chat-1", 6379); !res.Authorized || res.Raised {
			t.Fatalf("loopback in light posture: authorized=%v raised=%v, want silent", res.Authorized, res.Raised)
		}
	})

	t.Run("BalancedPostureIsSilentForListenAndCoGrantsLoopback", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{})
		b.listenOn(t, "chat-2", 8123)
		if res := b.connect(t, "chat-2", 8123); !res.Authorized || res.Raised {
			t.Fatalf("connect to the declared listener port: authorized=%v raised=%v, want silent", res.Authorized, res.Raised)
		}
		if res := b.connect(t, "chat-2", 8124); res.Authorized {
			t.Fatal("a listener grant co-authorized a port it did not declare")
		}
	})

	t.Run("BalancedPostureRequiresApprovalForForeignService", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{held: map[uint16]bool{6379: true}})
		if res := b.connect(t, "chat-3", 6379); res.Authorized {
			t.Fatal("foreign loopback connect in balanced posture must not be automatically authorized")
		}
	})
}

func TestPortBrokersProvenanceNuances(t *testing.T) {
	t.Parallel()

	t.Run("BalancedPostureIsSilentForChatOwnedService", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{owned: map[uint16]bool{3000: true}})
		if res := b.connect(t, "chat-owned", 3000); !res.Authorized || res.Raised {
			t.Fatalf("chat-owned service in balanced posture: authorized=%v raised=%v, want silent", res.Authorized, res.Raised)
		}
	})

	// Ownership feeds the verdict; it never records a lease a posture's ask
	// would then skip.
	t.Run("StrictDenialOfOwnedServiceSurvivesRetry", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureStrict, fixedProvenance{owned: map[uint16]bool{8000: true}})
		checkpoints := &cannedWriteRootCheckpoints{approve: false}
		b.loopback.Checkpoints = checkpoints
		first := b.connect(t, "chat-strict", 8000)
		if first.Authorized || !first.Raised || checkpoints.requested == nil {
			t.Fatalf("strict connect to an owned service: %+v, want a denied ask", first)
		}
		retry := b.connect(t, "chat-strict", 8000)
		if retry.Authorized {
			t.Fatalf("retry after a strict denial was authorized: %+v", retry)
		}
		if granted, _ := b.loopbackRT.SessionPorts("chat-strict"); granted {
			t.Fatal("a denied strict ask left a connect lease behind")
		}
	})
}

func TestListenCoGrantCoversOnlyDeclaredFreePorts(t *testing.T) {
	t.Parallel()

	t.Run("UnnarrowedListenCarriesNoConnectAuthority", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{held: map[uint16]bool{6379: true}})
		b.listenOn(t, "chat-any")
		if granted, ports := b.loopbackRT.SessionPorts("chat-any"); granted {
			t.Fatalf("listen on any port co-granted connect to %v", ports)
		}
		if res := b.connect(t, "chat-any", 6379); res.Authorized {
			t.Fatal("listen on any port made a foreign service reachable")
		}
	})

	t.Run("ListenOnAForeignPortCarriesNoConnectAuthority", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{held: map[uint16]bool{5432: true}})
		b.listenOn(t, "chat-db", 5432)
		if res := b.connect(t, "chat-db", 5432); res.Authorized {
			t.Fatal("declaring a listener on a foreign service's port made it reachable")
		}
	})

	t.Run("CombinedAskOnAForeignPortStillAsks", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{held: map[uint16]bool{5432: true}})
		res, err := b.network.AwaitCombined(context.Background(), tools.LocalNetworkAsk{
			SessionID: "chat-db", ProjectDir: "/tmp/proj", ListenPorts: []uint16{5432}, ConnectPorts: []uint16{5432},
		})
		if err != nil {
			t.Fatalf("AwaitCombined: %v", err)
		}
		if res.Authorized {
			t.Fatal("a combined listen+connect ask on a foreign service's port was silent")
		}
	})

	t.Run("CombinedAskOnAFreePortIsSilent", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{})
		res, err := b.network.AwaitCombined(context.Background(), tools.LocalNetworkAsk{
			SessionID: "chat-web", ProjectDir: "/tmp/proj", ListenPorts: []uint16{8123}, ConnectPorts: []uint16{8123},
		})
		if err != nil {
			t.Fatalf("AwaitCombined: %v", err)
		}
		if !res.Authorized || res.Raised {
			t.Fatalf("combined ask on a free port: %+v, want silent", res)
		}
	})
}

func sandboxServicePermission() *hitl.SecretPermission {
	recipient := secretmatch.Recipient{ID: "service", Label: "http://localhost:8080", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}
	grant := hitl.ApprovalGrant{ID: "secret", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "chat", ProjectID: "project",
		Predicate:          hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"value"})},
		SecretFingerprints: []string{"value"}, SecretRecipients: []secretmatch.Recipient{recipient}, Witness: hitl.SecretReleaseWitness([]secretmatch.Recipient{recipient})}
	return &hitl.SecretPermission{ConnectPorts: []uint16{8080}, Fingerprints: []secretmatch.SecretFingerprint{"value"},
		Screen: hitl.SecretScreen{Managed: true, SecretNames: []string{"Service password"}, Recipients: []secretmatch.Recipient{recipient}, DestinationID: recipient.ID, DestinationLabel: recipient.Label, DestinationKind: recipient.Kind, OriginKind: secretmatch.OriginField, SourcePath: "arguments"},
		Offers: []hitl.ApprovalGrantOffer{{ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: grant.Scope, Grant: grant, Coverage: "service password at the named origin"}},
	}
}

func TestLocalCapabilityCardsComposeSecretPermission(t *testing.T) {
	permission := sandboxServicePermission()
	loopback, err := (&LoopbackCheckpointBroker{}).buildCard(tools.LoopbackConnectAsk{
		ProjectID: "project", ToolName: "http_request", SecretPermission: permission,
	}, "chat", "chat", []uint16{8080})
	testutil.FailErr(t, "build combined HTTP permission", err)
	combined, err := (&LocalNetworkCheckpointBroker{}).buildCard(tools.LocalNetworkAsk{
		ProjectID: "project", ToolName: "command", ListenPorts: []uint16{8080}, ConnectPorts: []uint16{8080}, SecretPermission: permission,
	}, "chat", "chat")
	testutil.FailErr(t, "build combined process permission", err)
	for _, card := range []sandboxAskCard{loopback, combined} {
		connections := 0
		for _, target := range card.Plan.Subject.Targets {
			if target.Kind == "loopback_connect" {
				connections++
			}
		}
		if connections != 1 {
			t.Fatalf("combined card has %d connection targets, want one", connections)
		}
		selected, _ := card.Plan.Option(card.Plan.RecommendedOptionID)
		want := hitl.ApprovalRungChat
		if selected.Rung != want {
			t.Fatalf("primary=%s want %s", selected.Rung, want)
		}
		if card.Plan.Presentation.Location == nil {
			t.Fatal("compound disclosure lost its recipients")
		}
		for _, option := range card.Plan.Options {
			if !option.Disabled && option.Rung == hitl.ApprovalRungChat {
				grants := 0
				for _, delta := range option.Authority {
					if delta.Kind == hitl.AuthorityLoopbackConnectChat {
						grants++
					}
				}
				if grants != 1 {
					t.Fatalf("combined chat option installs %d connection grants, want one", grants)
				}
			}
			if option.Kind == hitl.ApprovalOptionQuiet && !option.Disabled {
				t.Fatal("a quiet could omit secret review")
			}
		}
	}
}

func TestNudgeCoordinatorScanDoneWakesOnlyTheSessionThatRequestedTheScan(t *testing.T) {
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)

	requester, err := store.Create(context.Background(), api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create requesting session", err)
	sameProject, err := store.Create(context.Background(), api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create same-project session", err)

	mgr.Coordinator.Scans.Wait = coordinatorcontrol.ScanWaitState{
		Requested: func(_ context.Context, sessionID, scanID string) bool {
			return sessionID == requester.ID && scanID == "scan-1"
		},
	}
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	deadline := time.Now().UTC().Add(10 * time.Minute)
	scanTriggers := []loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerScanDone}
	loop.Waits.EnterSleep(context.Background(), requester.ID, deadline, "waiting for scan", scanTriggers, nil, loopwake.SleepMoverHost)
	loop.Waits.EnterSleep(context.Background(), sameProject.ID, deadline, "waiting for scan", scanTriggers, nil, loopwake.SleepMoverHost)

	mgr.Coordinator.Scans.Finished(context.Background(), api.CodeScan{
		ID:            "scan-1",
		CanonicalPath: "/tmp/project-a",
		Status:        api.CodeScanStatusComplete,
	})

	if loop.Waits.IsSleeping(requester.ID) {
		t.Fatal("scan completion must wake the session that requested the scan")
	}
	if !loop.Waits.IsSleeping(sameProject.ID) {
		t.Fatal("scan completion must not wake a session in the same project that did not request it")
	}
}

type failingTurnClient struct{ failure error }

func (c failingTurnClient) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, c.failure
}

func (c failingTurnClient) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, c.failure
}

func TestFailedExecutionsReportOnceAndRetainTypedErrors(t *testing.T) {
	for _, entry := range []string{"prompt", "submission", "host"} {
		t.Run(entry, func(t *testing.T) {
			ctx := t.Context()
			mgr, st := newTestManager(t)
			provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
			mgr.Coordinator.Model.LLM = failingTurnClient{provider}
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create failure session", err)
			var delivered []error
			mgr.Runner.Turns.SetFailureSink(func(_ context.Context, id string, cause error) {
				if id != sess.ID {
					t.Errorf("failure session = %s", id)
				}
				delivered = append(delivered, cause)
			})
			switch entry {
			case "prompt":
				_, err = mgr.Submissions.Prompt(ctx, sess.ID, "Continue.")
			case "submission":
				in := promptinput.Input{Text: "Continue."}
				row, _, admitErr := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
				testutil.FailErr(t, "admit failure submission", admitErr)
				_, err = mgr.Submissions.RunPromptSubmission(ctx, row.ID)
			case "host":
				_, err = mgr.Submissions.LoopWake(ctx, sess.ID)
			}
			if len(delivered) != 1 || !errors.Is(delivered[0], provider) || !errors.Is(err, provider) {
				t.Fatalf("delivery = %v, returned = %v; want one typed failure", delivered, err)
			}
			if sessionexecution.UnreportedTurnFailure(err) != nil {
				t.Fatalf("caller would report the failure again: %v", err)
			}
		})
	}
}

func TestFailedTurnBlocksAutomaticAdmissionButAllowsExplicitRetry(t *testing.T) {
	mgr, st := newTestManager(t)
	provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
	mgr.Coordinator.Model.LLM = failingTurnClient{provider}
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	failures := 0
	mgr.Runner.Turns.SetFailureSink(func(context.Context, string, error) { failures++ })
	for attempt := 1; attempt <= 2; attempt++ {
		_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "Try again.")
		if !errors.Is(err, provider) || failures != attempt {
			t.Fatalf("explicit attempt %d: failures=%d error=%v", attempt, failures, err)
		}
		status, err := st.LatestTurnStatus(t.Context(), sess.ID)
		testutil.FailErr(t, "read failed turn", err)
		if status != store.TurnStatusFailed {
			t.Fatalf("turn status=%s", status)
		}
		loop := mgr.Coordinator.Runtime.CoordinatorLoop()
		loop.Nudges.Nudge(t.Context(), sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		if !loop.Nudges.HasPendingLoopWakes(sess.ID) {
			t.Fatal("failed turn discarded a new wake")
		}
		if !mgr.Admission.RoundComplete(t.Context(), sess.ID) {
			t.Fatal("held wake blocked new user input")
		}
		observed, err := mgr.Observations.Tree(t.Context(), sess.ID)
		testutil.FailErr(t, "observe settled failure", err)
		for _, blocker := range observed.Blockers {
			if blocker.Kind == "continuation" {
				t.Fatal("held wake reported active continuation")
			}
		}
		admitted := false
		_, err = mgr.Submissions.HostTurnWithAdmission(t.Context(), sess.ID, store.PromptSubmissionOriginLoopWake, promptinput.Input{}, func() error { admitted = true; return nil })
		testutil.FailErr(t, "held automatic turn", err)
		if admitted || failures != attempt {
			t.Fatalf("automatic retry: admitted=%v failures=%d", admitted, failures)
		}
	}
}
