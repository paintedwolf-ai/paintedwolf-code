package gate

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// A displayed link stays with the reader's contract only while the chat is
// known to hold no credential. After that, or when exposure is unknown, it is
// the agent's own request: reviewed once per host at Balanced and on every
// send at Strict.
func TestDisplayedLinkIsCoveredOnlyWhileNoCredentialIsHeld(t *testing.T) {
	type exposure struct {
		name            string
		known, exposed  bool
		coveredByReader bool
	}
	exposures := []exposure{
		{name: "none", known: true, coveredByReader: true},
		{name: "exposed", known: true, exposed: true},
		{name: "unknown"},
	}
	// first use, then exposure, then posture.
	want := map[bool]map[string]map[Posture]api.ApprovalGate{
		true: {
			"none":    {PostureStrict: api.GateFirstHost},
			"exposed": {PostureBalanced: api.GateAgentChosenOutbound, PostureStrict: api.GateSecretExposedOutbound},
			"unknown": {PostureBalanced: api.GateAgentChosenOutbound, PostureStrict: api.GateIncompleteFacts},
		},
		false: {
			"exposed": {PostureStrict: api.GateSecretExposedOutbound},
			"unknown": {PostureStrict: api.GateIncompleteFacts},
		},
	}
	for _, firstUse := range []bool{true, false} {
		for _, e := range exposures {
			for _, posture := range Postures() {
				f := baseFacts(StagePreDial)
				f.UntrustedIngested = true
				f.SecretExposed = e.exposed
				if !e.known {
					f.Ran &^= ProducerExposure
				}
				f.Destination = &Endpoint{Host: "page.example", Transport: "http", DisplayedLink: true, FirstUseThisSession: firstUse}
				if got := f.RetrievalCovers(); got != e.coveredByReader {
					t.Errorf("exposure %s: covered by the reader = %v", e.name, got)
				}
				verdict, decision := Evaluate(f, posture)
				expected, asks := want[firstUse][e.name][posture]
				switch {
				case asks && (verdict != Ask || !slices.Contains(decision.Gates(), expected)):
					t.Errorf("first use %v, exposure %s, %s: %v %+v, want %s", firstUse, e.name, posture, verdict, decision, expected)
				case !asks && verdict == Ask:
					t.Errorf("first use %v, exposure %s, %s: asked %v, want silent", firstUse, e.name, posture, decision.Gates())
				}
			}
		}
	}
}

// Exposure never withdraws the contract from a source, and an address the
// agent wrote never has it.
func TestRetrievalCoversSourcesWhateverTheExposure(t *testing.T) {
	for _, exposed := range []bool{false, true} {
		f := baseFacts(StagePreDial)
		f.SecretExposed = exposed
		f.Destination = &Endpoint{Host: "page.example", Retrieval: true}
		if !f.RetrievalCovers() {
			t.Errorf("exposed %v: a source lost the reader's contract", exposed)
		}
		f.Destination = &Endpoint{Host: "page.example"}
		if f.RetrievalCovers() {
			t.Errorf("exposed %v: an authored address gained the reader's contract", exposed)
		}
	}
	if (Facts{}).RetrievalCovers() {
		t.Error("no destination is covered")
	}
}

// A displayed link's ask cites the exposure that withdrew the contract, and
// an authored address's ask does not.
func TestDisplayedLinkAskCitesExposure(t *testing.T) {
	citesExposure := func(displayed bool) bool {
		f := baseFacts(StagePreDial)
		f.UntrustedIngested, f.SecretExposed = true, true
		f.Destination = &Endpoint{Host: "page.example", Transport: "http", DisplayedLink: displayed, FirstUseThisSession: true}
		verdict, decision := Evaluate(f, PostureBalanced)
		if verdict != Ask || decision.Primary != api.GateAgentChosenOutbound {
			t.Fatalf("displayed %v: %v %+v", displayed, verdict, decision)
		}
		return slices.ContainsFunc(decision.Cited, func(c Fact) bool { return c.Source == "exposure_ledger" })
	}
	if !citesExposure(true) {
		t.Error("a displayed link's ask must cite the chat's exposure")
	}
	if citesExposure(false) {
		t.Error("an authored address asks whatever the exposure, so it cites none")
	}
}
