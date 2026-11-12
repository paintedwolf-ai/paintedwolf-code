package toolexecution

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/testutil"
)

// fakeHostLedger answers SessionContacts from a fixed map and records every
// RecordContact call in order.
type fakeHostLedger struct {
	contacts map[string]evidence.Contact
	hosts    []string
	kinds    []evidence.Contact
}

func (f *fakeHostLedger) SessionContacts(string) map[string]evidence.Contact {
	out := make(map[string]evidence.Contact, len(f.contacts))
	for host, contact := range f.contacts {
		out[host] = contact
	}
	return out
}

func (f *fakeHostLedger) RecordContact(_ context.Context, _, host string, contact evidence.Contact) error {
	f.hosts = append(f.hosts, host)
	f.kinds = append(f.kinds, contact)
	return nil
}

func TestFirstUseThisSessionDecidesPerDialKind(t *testing.T) {
	retrievalDial := confine.EgressCommand{ToolName: ingestion.ToolFetchURL}
	ownDial := confine.EgressCommand{ToolName: "curl"}

	t.Run("missing ledger is first use", func(t *testing.T) {
		var nilNetwork *Network
		if !nilNetwork.firstUseThisSession(ownDial, "chat", "example.com", false) {
			t.Fatal("nil network must be first use")
		}
		if !(&Network{}).firstUseThisSession(retrievalDial, "chat", "example.com", false) {
			t.Fatal("unwired ledger must be first use")
		}
	})

	t.Run("retrieval dial is first use without any contact", func(t *testing.T) {
		for name, contact := range map[string]evidence.Contact{
			"none":      evidence.ContactNone,
			"retrieval": evidence.ContactRetrieval,
			"audited":   evidence.ContactAudited,
			"own":       evidence.ContactOwn,
		} {
			ledger := &fakeHostLedger{}
			if contact != evidence.ContactNone {
				ledger.contacts = map[string]evidence.Contact{"example.com": contact}
			}
			e := &Network{}
			e.SetSessionHostLedger(ledger)
			got := e.firstUseThisSession(retrievalDial, "chat", "example.com", false)
			if want := contact == evidence.ContactNone; got != want {
				t.Fatalf("%s: firstUse = %v, want %v", name, got, want)
			}
		}
	})

	t.Run("own dial is familiar only after the chat's own dial", func(t *testing.T) {
		for name, tc := range map[string]struct {
			contact evidence.Contact
			clean   bool
			want    bool
		}{
			"none":                        {evidence.ContactNone, false, true},
			"retrieval":                   {evidence.ContactRetrieval, false, true},
			"own":                         {evidence.ContactOwn, false, false},
			"audited, chat still clean":   {evidence.ContactAudited, true, false},
			"audited, chat now untrusted": {evidence.ContactAudited, false, true},
			"own, chat clean":             {evidence.ContactOwn, true, false},
		} {
			ledger := &fakeHostLedger{}
			if tc.contact != evidence.ContactNone {
				ledger.contacts = map[string]evidence.Contact{"example.com": tc.contact}
			}
			e := &Network{}
			e.SetSessionHostLedger(ledger)
			got := e.firstUseThisSession(ownDial, "chat", "example.com", tc.clean)
			if got != tc.want {
				t.Fatalf("%s: firstUse = %v, want %v", name, got, tc.want)
			}
		}
	})

	t.Run("host key is normalized", func(t *testing.T) {
		ledger := &fakeHostLedger{contacts: map[string]evidence.Contact{"example.com": evidence.ContactOwn}}
		e := &Network{}
		e.SetSessionHostLedger(ledger)
		if e.firstUseThisSession(ownDial, "chat", "  Example.COM ", false) {
			t.Fatal("normalized host must match the recorded contact")
		}
	})
}

func TestRecordHostVisitSeparatesDialKinds(t *testing.T) {
	retrievalDial := confine.EgressCommand{ToolName: ingestion.ToolWebSearch}
	ownDial := confine.EgressCommand{ToolName: "curl"}

	t.Run("own dial records the chat's own contact", func(t *testing.T) {
		ledger := &fakeHostLedger{}
		e := &Network{}
		e.SetSessionHostLedger(ledger)
		e.recordHostVisit(t.Context(), ownDial, "chat", "Example.com")
		if !slices.Equal(ledger.hosts, []string{"Example.com"}) || !slices.Equal(ledger.kinds, []evidence.Contact{evidence.ContactOwn}) {
			t.Fatalf("recorded hosts = %v contacts = %v", ledger.hosts, ledger.kinds)
		}
	})

	t.Run("a clean chat's allowed dial records only its audit", func(t *testing.T) {
		ledger := &fakeHostLedger{}
		e := &Network{}
		e.SetSessionHostLedger(ledger)
		e.recordAuditedVisit(t.Context(), ownDial, "chat", "example.com")
		e.recordAuditedVisit(t.Context(), retrievalDial, "chat", "source.example.net")
		if !slices.Equal(ledger.kinds, []evidence.Contact{evidence.ContactAudited, evidence.ContactRetrieval}) {
			t.Fatalf("recorded contacts = %v", ledger.kinds)
		}
	})

	t.Run("retrieval dial records the retrieval contact", func(t *testing.T) {
		ledger := &fakeHostLedger{}
		e := &Network{}
		e.SetSessionHostLedger(ledger)
		e.recordHostVisit(t.Context(), retrievalDial, "chat", "source.example.net")
		if !slices.Equal(ledger.hosts, []string{"source.example.net"}) || !slices.Equal(ledger.kinds, []evidence.Contact{evidence.ContactRetrieval}) {
			t.Fatalf("recorded hosts = %v contacts = %v", ledger.hosts, ledger.kinds)
		}
	})

	t.Run("missing ledger records nothing", func(t *testing.T) {
		var nilNetwork *Network
		nilNetwork.recordHostVisit(t.Context(), ownDial, "chat", "example.com")
		ledger := &fakeHostLedger{}
		e := &Network{}
		e.recordHostVisit(t.Context(), ownDial, "chat", "example.com")
		if len(ledger.hosts) != 0 {
			t.Fatalf("unwired ledger recorded %v", ledger.hosts)
		}
	})
}

func TestSessionHostLedgerSetterWiresTheLedger(t *testing.T) {
	ledger := &fakeHostLedger{contacts: map[string]evidence.Contact{"example.com": evidence.ContactOwn}}
	e := &Network{}
	e.SetSessionHostLedger(ledger)
	if e.hostLedger == nil {
		t.Fatal("SetSessionHostLedger left the ledger unwired")
	}
	testutil.FailErr(t, "RecordContact", e.hostLedger.RecordContact(t.Context(), "chat", "example.com", evidence.ContactOwn))
	if !slices.Equal(ledger.hosts, []string{"example.com"}) {
		t.Fatalf("recorded hosts = %v", ledger.hosts)
	}
}

// A source reader's request for an address the agent wrote is the chat's own
// dial: only the chat's own contact makes its host familiar, it is recorded as
// one, and the gate does not see a retrieval.
func TestAuthoredRetrievalAddressIsTheChatsOwnDial(t *testing.T) {
	authored := confine.EgressCommand{Image: ingestion.ToolFetchURL, AuthoredDestination: true}
	observed := confine.EgressCommand{Image: ingestion.ToolFetchURL}
	ep := egressproxy.Endpoint{Host: "example.com", Port: 443, Transport: egressproxy.TransportHTTPRequest}

	ledger := &fakeHostLedger{contacts: map[string]evidence.Contact{"example.com": evidence.ContactRetrieval}}
	e := &Network{}
	e.SetSessionHostLedger(ledger)
	if !e.firstUseThisSession(authored, "chat", "example.com", false) {
		t.Fatal("a retrieved source must not make the agent's authored request familiar")
	}
	if e.firstUseThisSession(observed, "chat", "example.com", false) {
		t.Fatal("a repeat retrieval of the same host must stay familiar")
	}

	e.recordHostVisit(t.Context(), authored, "chat", "example.com")
	e.recordHostVisit(t.Context(), observed, "chat", "example.com")
	if !slices.Equal(ledger.kinds, []evidence.Contact{evidence.ContactOwn, evidence.ContactRetrieval}) {
		t.Fatalf("recorded contacts = %v", ledger.kinds)
	}

	if destinationFact(authored, ep, true, "", "").Retrieval {
		t.Fatal("an authored address reached the gate as a retrieval")
	}
	if !destinationFact(observed, ep, true, "", "").Retrieval {
		t.Fatal("an observed address lost its retrieval fact")
	}
	if got := endpointAskSubject(authored, ep).Explanation.Who; got != hitl.WhoAgentAction {
		t.Fatalf("a tool's own request is an action, got %q", got)
	}
	command := confine.EgressCommand{ToolName: "command", CommandLine: "curl https://example.com/"}
	if got := endpointAskSubject(command, ep).Explanation.Who; got != hitl.WhoAgentCommand {
		t.Fatalf("a command line is a command, got %q", got)
	}
}
