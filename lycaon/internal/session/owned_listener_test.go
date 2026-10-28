package session

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

type exactListenerFixture struct {
	fixedProvenance
	id  string
	err error
}

func (f exactListenerFixture) ObserveSessionListener(context.Context, string, netip.AddrPort) (string, bool, error) {
	return f.id, true, f.err
}

type portOnlyFixture struct{ LoopbackProvenanceResolver }

func TestOwnedListenerRequiresExactCompleteObservation(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	for _, fixture := range []LoopbackProvenanceResolver{nil, portOnlyFixture{}, exactListenerFixture{}, exactListenerFixture{id: "partial", err: errors.New("incomplete")}} {
		if _, ok := ObserveOwnedSessionListener(t.Context(), fixture, "session", endpoint); ok {
			t.Fatal("missing/incomplete/exclusively port-based evidence granted access")
		}
	}
	fixture := exactListenerFixture{id: "resource"}
	if id, ok := ObserveOwnedSessionListener(t.Context(), fixture, "session", endpoint); !ok || id != "resource" {
		t.Fatal("exact resource lost")
	}
	for _, value := range []string{"0.0.0.0:8123", "192.0.2.1:8123", "127.0.0.1:0"} {
		if _, ok := ObserveOwnedSessionListener(t.Context(), fixture, "session", netip.MustParseAddrPort(value)); ok {
			t.Fatal("nonexact loopback accepted")
		}
	}
}

func TestDefaultListenerObservationUsesFreshNativeIdentity(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	identity := "native-first-incarnation"
	calls := 0
	fixture := &provenanceFixture{
		now:      []socketListener{listenerAt(4242, endpoint.String())},
		sessions: map[int]string{4242: "session"},
		nativeObserver: func(_ context.Context, session string, actual netip.AddrPort) (string, bool, error) {
			calls++
			if session != "session" || actual != endpoint {
				t.Fatalf("native observation = %q %v", session, actual)
			}
			return identity, true, nil
		},
	}
	resolver := fixture.resolver(t.Context())
	mapped := netip.MustParseAddrPort("[::ffff:127.0.0.1]:8123")
	first, owned, err := resolver.ObserveSessionListener(t.Context(), "session", mapped)
	testutil.FailErr(t, "observe canonical native listener", err)
	if !owned || first != identity {
		t.Fatalf("native identity was replaced: %q %v", first, owned)
	}
	identity = "native-replaced-incarnation"
	second, owned, err := resolver.ObserveSessionListener(t.Context(), "session", endpoint)
	testutil.FailErr(t, "observe replacement with the same numeric PID", err)
	if !owned || second != identity || first == second || calls != 2 {
		t.Fatalf("stale native identity: %q %v calls=%d", second, owned, calls)
	}
	for _, other := range []socketListener{listenerAt(900, "[::1]:8123"), listenerAt(0, "[::1]:8123")} {
		fixture.now = []socketListener{listenerAt(4242, endpoint.String()), other}
		if _, ok := ObserveOwnedSessionListener(t.Context(), resolver, "session", endpoint); ok {
			t.Fatal("foreign or incomplete co-holder granted access")
		}
	}
	fixture.now = []socketListener{listenerAt(4242, "[::1]:8123")}
	if _, ok := ObserveOwnedSessionListener(t.Context(), resolver, "session", endpoint); ok {
		t.Fatal("another loopback address became the requested endpoint")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, owned, err := resolver.ObserveSessionListener(ctx, "session", endpoint); owned || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled observation = owned:%v err:%v", owned, err)
	}
	if calls != 2 {
		t.Fatalf("invalid all-port evidence reached native observation: calls=%d", calls)
	}
}

func TestDefaultListenerObservationCannotFallBackFromNativeRefusal(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	for _, nativeErr := range []error{nil, lineage.ErrObservationIncomplete, lineage.ErrUnsupported} {
		fixture := &provenanceFixture{
			now: []socketListener{listenerAt(4242, endpoint.String())}, sessions: map[int]string{4242: "session"},
			nativeObserver: func(context.Context, string, netip.AddrPort) (string, bool, error) { return "", false, nativeErr },
		}
		resolver := fixture.resolver(t.Context())
		id, owned, err := resolver.ObserveSessionListener(t.Context(), "session", endpoint)
		if id != "" || owned || !errors.Is(err, nativeErr) {
			t.Fatalf("native refusal became PID-only proof: id=%q owned=%v err=%v", id, owned, err)
		}
		if _, ok := ObserveOwnedSessionListener(t.Context(), resolver, "session", endpoint); ok {
			t.Fatal("incomplete native evidence bypassed ordinary approval")
		}
	}
	var missingResolver *DefaultLoopbackProvenance
	if _, owned, err := missingResolver.ObserveSessionListener(t.Context(), "session", endpoint); owned || !errors.Is(err, lineage.ErrObservationIncomplete) {
		t.Fatalf("missing resolver = owned:%v err:%v", owned, err)
	}
	missingTable := &DefaultLoopbackProvenance{}
	if _, owned, err := missingTable.ObserveSessionListener(t.Context(), "session", endpoint); owned || !errors.Is(err, lineage.ErrObservationIncomplete) {
		t.Fatalf("missing all-port producer = owned:%v err:%v", owned, err)
	}
	fixture := &provenanceFixture{now: []socketListener{listenerAt(4242, endpoint.String())}, sessions: map[int]string{4242: "session"}}
	_, owned, err := fixture.resolver(t.Context()).ObserveSessionListener(t.Context(), "session", endpoint)
	if owned || !errors.Is(err, lineage.ErrObservationIncomplete) {
		t.Fatalf("missing native producer = owned:%v err:%v", owned, err)
	}
}

func TestPublicLoopbackResolverUsesNativeObservationWithListenerTable(t *testing.T) {
	resolver := NewLoopbackProvenance(t.Context())
	if resolver.listeners == nil || resolver.observeNative == nil {
		t.Fatal("public constructor omitted an observation source")
	}
	session := "no-native-lineage-" + t.Name()
	resolver.listeners = func(context.Context) ([]socketListener, error) {
		return []socketListener{listenerAt(4242, "127.0.0.1:8123")}, nil
	}
	resolver.lineageSession = func(int) (string, bool) { return session, true }
	// Keep the real native producer while making the PID table positive.
	if id, owned, err := resolver.ObserveSessionListener(t.Context(), session, netip.MustParseAddrPort("127.0.0.1:8123")); (err != nil && !errors.Is(err, lineage.ErrUnsupported)) || owned || id != "" {
		t.Fatalf("public resolver substituted PID-only proof: id=%q owned=%v err=%v", id, owned, err)
	}
}

func TestDefaultListenerObservationPreservesRecordedContainerChecks(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	containerID := strings.Repeat("a", 64)
	calls := 0
	fixture := &provenanceFixture{
		containers: []dockerContainerInfo{{ID: containerID, Name: "registered", Ports: []containerPortMapping{{PublicPort: 8123, LoopbackOnly: true}}}},
		nativeObserver: func(context.Context, string, netip.AddrPort) (string, bool, error) {
			calls++
			return "", false, lineage.ErrObservationIncomplete
		},
	}
	resolver := fixture.resolver(t.Context())
	resolver.RecordSessionContainer("session", containerID, "")
	if id, owned, err := resolver.ObserveSessionListener(t.Context(), "session", endpoint); err != nil || !owned || id == "" {
		t.Fatalf("recorded container lost current evidence: %q %v %v", id, owned, err)
	}
	fixture.containers[0].Ports[0].LoopbackOnly = false
	if _, owned, _ := resolver.ObserveSessionListener(t.Context(), "session", endpoint); owned {
		t.Fatal("exposed container granted access")
	}
	fixture.containers[0].Ports[0].LoopbackOnly = true
	fixture.containers[0].ID = strings.Repeat("b", 64)
	if _, owned, _ := resolver.ObserveSessionListener(t.Context(), "session", endpoint); owned {
		t.Fatal("unrecorded replacement container granted access")
	}
	fixture.containers[0].ID = containerID
	fixture.now = []socketListener{listenerAt(0, endpoint.String())}
	if _, owned, err := resolver.ObserveSessionListener(t.Context(), "session", endpoint); owned || err == nil {
		t.Fatalf("incomplete holder became container proof: %v %v", owned, err)
	}
	if calls != 0 {
		t.Fatalf("container evidence impersonated native lineage: calls=%d", calls)
	}
}
