package gate

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Raising posture preserves every gate already enabled.
func TestPostureLadderIsMonotone(t *testing.T) {
	t.Parallel()
	ordered := Postures()
	for i := 1; i < len(ordered); i++ {
		lower, higher := ordered[i-1], ordered[i]
		if lower.Strictness() >= higher.Strictness() {
			t.Fatalf("Postures() is not ordered by Strictness: %s(%d) then %s(%d)",
				lower, lower.Strictness(), higher, higher.Strictness())
		}
		for _, g := range lower.Gates() {
			if !higher.Enables(g) {
				t.Errorf("%s runs %s but the stricter %s does not", lower, g, higher)
			}
		}
	}
}

// Gate order determines the primary reason shown on a card.
func TestPostureGatesAreInCitationPriorityOrder(t *testing.T) {
	t.Parallel()
	priority := map[api.ApprovalGate]int{}
	for i, g := range api.AllApprovalGateValues() {
		priority[g] = i
	}
	for _, p := range Postures() {
		live := p.Gates()
		if len(live) == 0 {
			t.Fatalf("%s runs no gates", p)
		}
		for i := 1; i < len(live); i++ {
			if priority[live[i-1]] >= priority[live[i]] {
				t.Errorf("%s.Gates() = %v: %s outranks %s in the vocabulary but follows it here",
					p, live, live[i], live[i-1])
			}
		}
		for _, g := range live {
			if !p.Enables(g) {
				t.Errorf("%s.Gates() returned %s, which %s does not enable", p, g, p)
			}
		}
		if len(live) != len(postureGates[p]) {
			t.Errorf("%s.Gates() returned %d gates, the membership row holds %d", p, len(live), len(postureGates[p]))
		}
	}
}

// Unknown tokens use the default posture.
func TestPostureFromStringNeverReadsAsAnOptOut(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "  ", "paranoid", "off", "none", "LIGHTS"} {
		if got := PostureFromString(token); got != DefaultPosture {
			t.Errorf("PostureFromString(%q) = %q want %q", token, got, DefaultPosture)
		}
	}
	for _, p := range Postures() {
		if got := PostureFromString(string(p)); got != p {
			t.Errorf("PostureFromString(%q) = %q want round trip", p, got)
		}
		if !ValidPosture(string(p)) {
			t.Errorf("ValidPosture(%q) = false", p)
		}
	}
	if ValidPosture("paranoid") {
		t.Error("ValidPosture accepted an unknown token — an API boundary would coerce it silently")
	}
}

// Stricter is how a project layer raises the ask-line without being able to lower it.
func TestStricterNeverLowersASetPosture(t *testing.T) {
	t.Parallel()
	cases := []struct{ a, b, want Posture }{
		{PostureLight, PostureStrict, PostureStrict},
		{PostureStrict, PostureLight, PostureStrict},
		{PostureBalanced, PostureLight, PostureBalanced},
		{"", PostureLight, PostureLight},
		{PostureStrict, "", PostureStrict},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := Stricter(tc.a, tc.b); got != tc.want {
			t.Errorf("Stricter(%q, %q) = %q want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// The egress default is derived, so the network side cannot disagree with the card side.
func TestAsksOnFirstHostFollowsTheGateTable(t *testing.T) {
	t.Parallel()
	for _, p := range Postures() {
		if got, want := p.AsksOnFirstHost(), p.Enables(api.GateFirstHost); got != want {
			t.Errorf("%s.AsksOnFirstHost() = %v want %v", p, got, want)
		}
	}
	if !PostureStrict.AsksOnFirstHost() {
		t.Error("strict must ask before the first reach to a new host")
	}
	if PostureBalanced.AsksOnFirstHost() {
		t.Error("balanced must let egress flow")
	}
}

// A stricter posture never recommends a broader subject or duration.
func TestPostureLadderShapeIsMonotone(t *testing.T) {
	t.Parallel()
	widenRank := map[Widening]int{WidenFromFirstCard: 0, WidenMenuOnly: 1}
	ordered := Postures()
	for i := 1; i < len(ordered); i++ {
		lower, higher := ordered[i-1].Ladder(), ordered[i].Ladder()
		if widenRank[higher.Widening] < widenRank[lower.Widening] {
			t.Errorf("%s widens sooner (%s) than %s (%s)", ordered[i], higher.Widening, ordered[i-1], lower.Widening)
		}
	}
	if PostureStrict.Widens() {
		t.Fatal("Strict must never face a wider unit")
	}
	if !PostureLight.Widens() {
		t.Fatal("Light faces the wider unit from the first card")
	}
	if PostureBalanced.Widens() {
		t.Fatal("Balanced keeps broader subjects in the menu")
	}
	if got := Posture("garbage").Ladder(); got != PostureBalanced.Ladder() {
		t.Fatalf("unknown posture ladder = %+v, want Balanced's", got)
	}
}

// Every decision carries the posture it was evaluated under, so the card shaping
// that follows cannot read a different dial than the verdict did.
func TestDecisionCarriesItsPosture(t *testing.T) {
	t.Parallel()
	f := Facts{Stage: StagePreDial, Ran: ProducerContainment | ProducerDestination | ProducerLease | ProducerRule | ProducerDetection,
		Destination: &Endpoint{Host: "example.com", Port: 443, Transport: "http_connect", Opaque: true, FirstUseThisSession: true}}
	verdict, decision := Evaluate(f, PostureStrict)
	if verdict != Ask || decision == nil {
		t.Fatalf("expected an ask, got %s", verdict)
	}
	if decision.Posture != PostureStrict {
		t.Fatalf("decision posture = %q, want strict", decision.Posture)
	}
	_, unreported := Evaluate(Facts{Stage: StagePreSpawn}, PostureLight)
	if unreported == nil || unreported.Posture != PostureLight {
		t.Fatalf("incomplete-facts decision must carry the posture too: %+v", unreported)
	}
}
