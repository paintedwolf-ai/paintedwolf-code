package gate

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func ingestedPreDial(firstUse bool) Facts {
	f := baseFacts(StagePreDial)
	f.Ran |= ProducerIngestion
	f.UntrustedIngested = true
	f.Destination = &Endpoint{
		Host: "paste.example", Transport: "http", Opaque: true, FirstUseThisSession: firstUse,
	}
	return f
}

// Ingestion is cited on the destination gate rather than raising one of its own,
// so a person gets one reason with one subject.
func TestIngestionIsCitedOnTheDestinationGate(t *testing.T) {
	t.Parallel()
	verdict, decision := Evaluate(ingestedPreDial(true), PostureBalanced)
	if verdict != Ask {
		t.Fatalf("verdict=%s, want ask", verdict)
	}
	if decision.Primary != api.GateAgentChosenOutbound {
		t.Fatalf("primary=%s, want agent_chosen_outbound", decision.Primary)
	}
	if len(decision.Gates()) != 1 {
		t.Fatalf("gates=%v — ingestion must not raise a second gate beside the destination", decision.Gates())
	}
	var found bool
	for _, c := range decision.Cited {
		if c.Key == "session.state" && strings.Contains(c.Value, "did not author") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ingestion state was not cited: %+v", decision.Cited)
	}
}

// The destination gate goes quiet once the host is recorded; ingestion state
// must not reopen it.
func TestIngestionDoesNotReAskOnAnAlreadyReachedHost(t *testing.T) {
	t.Parallel()
	verdict, decision := Evaluate(ingestedPreDial(false), PostureBalanced)
	if verdict != Silent {
		t.Fatalf("verdict=%s with decision %v — a host already reached in this chat must not ask again", verdict, decision.Gates())
	}
}

// A lease still settles it.
func TestIngestionRespectsAnExistingLease(t *testing.T) {
	t.Parallel()
	f := ingestedPreDial(true)
	f.Leased = true
	if verdict, _ := Evaluate(f, PostureBalanced); verdict != Silent {
		t.Fatalf("verdict=%s, want silent under an existing lease", verdict)
	}
}

// A chat that has read nothing external gets the card without the extra claim.
func TestUningestedChatCardMakesNoSessionClaim(t *testing.T) {
	t.Parallel()
	f := ingestedPreDial(true)
	f.UntrustedIngested = false
	_, decision := Evaluate(f, PostureBalanced)
	for _, c := range decision.Cited {
		if c.Key == "session.state" {
			t.Fatalf("card claims %q for a chat that ingested nothing", c.Value)
		}
	}
}

// An unwired producer is not established, never clean: the card must not state a
// session fact nobody read.
func TestUnreportedIngestionIsNotCited(t *testing.T) {
	t.Parallel()
	f := ingestedPreDial(true)
	f.Ran &^= ProducerIngestion
	_, decision := Evaluate(f, PostureBalanced)
	for _, c := range decision.Cited {
		if c.Key == "session.state" {
			t.Fatalf("card cited session state the producer never reported: %q", c.Value)
		}
	}
}
