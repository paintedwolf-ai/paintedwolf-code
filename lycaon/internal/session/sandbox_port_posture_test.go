package session

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
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

func (f fixedProvenance) ObserveSessionListener(_ context.Context, _ string, endpoint netip.AddrPort) (string, bool, error) {
	if f.owned[endpoint.Port()] {
		return "fixture-resource", true, nil
	}
	return "", false, nil
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
		listen:   &ListenCheckpointBroker{Runtime: listenRT, Posture: postureOf(posture)},
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

	t.Run("BalancedListenRequestDoesNotAuthorizeUnboundConnect", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{})
		b.listenOn(t, "chat-2", 8123)
		if res := b.connect(t, "chat-2", 8123); res.Authorized {
			t.Fatal("listen request authorized connect before an owned listener existed")
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
		if res := b.connect(t, "chat-owned", 3000); !res.Authorized || res.Raised || !res.RequiresOwnedListener {
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

func TestListenRequestsNeverSupplyConnectAuthority(t *testing.T) {
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

	t.Run("CombinedAskOnAFreePortRequiresConnectApproval", func(t *testing.T) {
		t.Parallel()

		b := newPortBrokers(gate.PostureBalanced, fixedProvenance{})
		res, err := b.network.AwaitCombined(context.Background(), tools.LocalNetworkAsk{
			SessionID: "chat-web", ProjectDir: "/tmp/proj", ListenPorts: []uint16{8123}, ConnectPorts: []uint16{8123},
		})
		if err != nil {
			t.Fatalf("AwaitCombined: %v", err)
		}
		if res.Authorized {
			t.Fatalf("combined free-port request bypassed independent connect approval: %+v", res)
		}
	})
}

func TestAutomaticOwnedLoopbackNeverCreatesAStickyLease(t *testing.T) {
	owned := map[uint16]bool{8123: true}
	brokers := newPortBrokers(gate.PostureBalanced, fixedProvenance{owned: owned})
	first := brokers.connect(t, "fixture-chat", 8123)
	if !first.Authorized || !first.RequiresOwnedListener {
		t.Fatalf("owned observation lost mediated condition: %+v", first)
	}
	if granted, ports := brokers.loopbackRT.SessionPorts("fixture-chat"); granted {
		t.Fatalf("ownership observation cached connect ports %v", ports)
	}
	delete(owned, 8123)
	if next := brokers.connect(t, "fixture-chat", 8123); next.Authorized {
		t.Fatal("closed or foreign-rebound listener retained automatic authority")
	}
	brokers.loopbackRT.GrantSessionPorts("fixture-chat", []uint16{8123})
	explicit := brokers.connect(t, "fixture-chat", 8123)
	if !explicit.Authorized || explicit.RequiresOwnedListener {
		t.Fatalf("independent approved connect authority was lost: %+v", explicit)
	}
}

type endpointProvenance struct {
	fixedProvenance
	endpoints map[netip.AddrPort]struct {
		id    string
		owned bool
		err   error
	}
}

func (p endpointProvenance) ObserveSessionListener(_ context.Context, _ string, endpoint netip.AddrPort) (string, bool, error) {
	obs, ok := p.endpoints[endpoint]
	if !ok {
		return "", false, nil
	}
	return obs.id, obs.owned, obs.err
}

func TestLoopbackAllHolderConvergenceAndIncompleteObservation(t *testing.T) {
	t.Parallel()
	v4 := netip.MustParseAddrPort("127.0.0.1:8123")
	v6 := netip.MustParseAddrPort("[::1]:8123")

	t.Run("AllLoopbackHoldersOwned", func(t *testing.T) {
		t.Parallel()
		prov := endpointProvenance{
			endpoints: map[netip.AddrPort]struct {
				id    string
				owned bool
				err   error
			}{
				v4: {id: "res-v4", owned: true},
				v6: {id: "res-v6", owned: true},
			},
		}
		b := newPortBrokers(gate.PostureBalanced, prov)
		if res := b.connect(t, "chat-dual", 8123); !res.Authorized || !res.RequiresOwnedListener {
			t.Fatalf("converged dual-stack owned listeners were not admitted: %+v", res)
		}
	})

	t.Run("SecondForeignHolderRequiresApproval", func(t *testing.T) {
		t.Parallel()
		prov := endpointProvenance{
			endpoints: map[netip.AddrPort]struct {
				id    string
				owned bool
				err   error
			}{
				v4: {id: "res-v4", owned: true},
				v6: {id: "res-foreign", owned: false},
			},
		}
		b := newPortBrokers(gate.PostureBalanced, prov)
		if res := b.connect(t, "chat-shared", 8123); res.Authorized {
			t.Fatalf("port shared with foreign holder bypassed approval: %+v", res)
		}
		combined, err := b.network.AwaitCombined(context.Background(), tools.LocalNetworkAsk{
			SessionID: "chat-shared", ProjectDir: "/tmp/proj", ListenPorts: []uint16{8123}, ConnectPorts: []uint16{8123},
		})
		if err != nil || combined.Authorized {
			t.Fatalf("combined ask on shared port bypassed approval: %+v err=%v", combined, err)
		}
	})

	t.Run("HeldByOtherBlocksPartialFamilyOwnership", func(t *testing.T) {
		t.Parallel()
		prov := endpointProvenance{
			fixedProvenance: fixedProvenance{held: map[uint16]bool{8123: true}},
			endpoints: map[netip.AddrPort]struct {
				id    string
				owned bool
				err   error
			}{
				v4: {id: "res-v4", owned: true},
			},
		}
		b := newPortBrokers(gate.PostureBalanced, prov)
		if res := b.connect(t, "chat-held", 8123); res.Authorized {
			t.Fatalf("port held by other bypassed approval: %+v", res)
		}
	})

	t.Run("IncompleteObservationRetainsOrdinaryApproval", func(t *testing.T) {
		t.Parallel()
		prov := endpointProvenance{
			endpoints: map[netip.AddrPort]struct {
				id    string
				owned bool
				err   error
			}{
				v4: {id: "res-v4", owned: true},
				v6: {err: errors.New("incomplete holder observation")},
			},
		}
		b := newPortBrokers(gate.PostureBalanced, prov)
		if res := b.connect(t, "chat-partial", 8123); res.Authorized {
			t.Fatalf("incomplete listener observation bypassed approval: %+v", res)
		}
		combined, err := b.network.AwaitCombined(context.Background(), tools.LocalNetworkAsk{
			SessionID: "chat-partial", ProjectDir: "/tmp/proj", ListenPorts: []uint16{8123}, ConnectPorts: []uint16{8123},
		})
		if err != nil || combined.Authorized {
			t.Fatalf("combined ask with incomplete observation bypassed approval: %+v err=%v", combined, err)
		}
	})

	t.Run("DefaultLoopbackProvenanceExactSessionListener", func(t *testing.T) {
		t.Parallel()
		nativeIdentity := "native-before"
		fixture := &provenanceFixture{
			now:            []socketListener{listenerAt(4242, "127.0.0.1:8123"), listenerAt(4243, "[::1]:8123")},
			sessions:       map[int]string{4242: "chat-1", 4243: "chat-1"},
			nativeObserver: func(context.Context, string, netip.AddrPort) (string, bool, error) { return nativeIdentity, true, nil },
		}
		prov := fixture.resolver(t.Context())
		b := newPortBrokers(gate.PostureBalanced, prov)
		res := b.connect(t, "chat-1", 8123)
		if !res.Authorized || !res.RequiresOwnedListener {
			t.Fatalf("DefaultLoopbackProvenance dual-stack owned listeners rejected: %+v", res)
		}
		beforeID, ok := ObserveOwnedSessionListener(t.Context(), prov, "chat-1", v4)
		if !ok || beforeID == "" {
			t.Fatalf("ObserveOwnedSessionListener(v4) = %q, %v; want owned resource ID", beforeID, ok)
		}

		// Close and rebind by another session process changes the resource ID, revoking prior dial observation.
		fixture.now = []socketListener{listenerAt(4244, "127.0.0.1:8123")}
		fixture.sessions[4244] = "chat-1"
		nativeIdentity = "native-replacement"
		afterID, ok := ObserveOwnedSessionListener(t.Context(), prov, "chat-1", v4)
		if !ok || afterID == "" || afterID == beforeID {
			t.Fatalf("rebind resource ID = %q (ok=%v), want distinct from %q", afterID, ok, beforeID)
		}

		// Second foreign holder on IPv6 revokes automatic ownership across the port.
		fixture.now = []socketListener{listenerAt(4244, "127.0.0.1:8123"), listenerAt(900, "[::1]:8123")}
		if res := b.connect(t, "chat-1", 8123); res.Authorized {
			t.Fatalf("foreign holder on IPv6 bypassed approval: %+v", res)
		}
		if id, ok := ObserveOwnedSessionListener(t.Context(), prov, "chat-1", v4); ok || id != "" {
			t.Fatalf("foreign co-holder still observed as owned: id=%q ok=%v", id, ok)
		}

		// Incomplete holder PID observation retains ordinary approval.
		fixture.now = []socketListener{listenerAt(4244, "127.0.0.1:8123"), listenerAt(0, "[::1]:8123")}
		if res := b.connect(t, "chat-1", 8123); res.Authorized {
			t.Fatalf("incomplete holder PID bypassed approval: %+v", res)
		}
	})
}
