package gate

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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
		floored := 0
		for _, floor := range gateFloor {
			if floor.Strictness() <= p.Strictness() {
				floored++
			}
		}
		if len(live) != floored {
			t.Errorf("%s.Gates() returned %d gates, the floor table admits %d", p, len(live), floored)
		}
	}
}

// Only the three canonical tokens parse; a typo is an error, never a posture.
func TestParsePostureAcceptsOnlyCanonicalTokens(t *testing.T) {
	t.Parallel()
	for _, p := range Postures() {
		if got, err := ParsePosture(string(p)); err != nil || got != p {
			t.Errorf("ParsePosture(%q) = %q, %v; want a round trip", p, got, err)
		}
	}
	for _, token := range []string{"", " strict", "Strict", "Ballanced", "paranoid", "off", "none"} {
		if got, err := ParsePosture(token); err == nil {
			t.Errorf("ParsePosture(%q) = %q, want an error", token, got)
		}
	}
}

// Decoding is where hand-edited files and wire input become postures, so it
// applies the same parser; an empty value is an unset layer.
func TestDecodedPostureRefusesUnknownTokens(t *testing.T) {
	t.Parallel()
	type doc struct {
		Posture Posture `yaml:"approval_posture" json:"approval_posture"`
	}
	var fromYAML doc
	if err := yaml.Unmarshal([]byte("approval_posture: strict\n"), &fromYAML); err != nil || fromYAML.Posture != PostureStrict {
		t.Fatalf("yaml strict = %q, %v", fromYAML.Posture, err)
	}
	if err := yaml.Unmarshal([]byte("approval_posture: Ballanced\n"), &fromYAML); err == nil || !strings.Contains(err.Error(), "Ballanced") {
		t.Fatalf("yaml typo decoded without naming it: %v", err)
	}
	var fromJSON doc
	if err := json.Unmarshal([]byte(`{"approval_posture":"lite"}`), &fromJSON); err == nil {
		t.Fatalf("json typo decoded as %q", fromJSON.Posture)
	}
	if err := json.Unmarshal([]byte(`{"approval_posture":""}`), &fromJSON); err != nil || fromJSON.Posture != "" {
		t.Fatalf("empty json posture = %q, %v; want unset", fromJSON.Posture, err)
	}
}

// A value that bypassed parsing can only add asks.
func TestUnparsedPostureReadsAsStrict(t *testing.T) {
	t.Parallel()
	for _, p := range []Posture{"", "garbage", "Light"} {
		if p.Strictness() != PostureStrict.Strictness() || p.Ladder() != PostureStrict.Ladder() {
			t.Errorf("%q does not read as strict", p)
		}
		for _, g := range PostureStrict.Gates() {
			if !p.Enables(g) {
				t.Errorf("%q does not run strict's %s", p, g)
			}
		}
		if p.ReleasesChatSecretLocally(&SecretHit{ChatGenerated: true, RecipientsLocal: true}) {
			t.Errorf("%q releases a chat secret without a card", p)
		}
	}
	f := Facts{Stage: StagePreDial, Ran: ProducerContainment | ProducerDestination | ProducerLease | ProducerRule | ProducerDetection,
		Destination: &Endpoint{Host: "example.com", Port: 443, Transport: "http_connect", Opaque: true, FirstUseThisSession: true}}
	if verdict, decision := Evaluate(f, "garbage"); verdict != Ask || decision.Posture != PostureStrict {
		t.Fatalf("unparsed posture evaluated as %s/%+v, want a strict ask", verdict, decision)
	}
}

// Every gate except missing facts has a quietest posture that runs it.
func TestEveryGateHasAPostureFloor(t *testing.T) {
	t.Parallel()
	for _, g := range api.AllApprovalGateValues() {
		floor, ok := gateFloor[g]
		if g == api.GateIncompleteFacts {
			if ok {
				t.Errorf("%s asks at every posture and must not have a floor", g)
			}
			continue
		}
		if !ok {
			t.Errorf("%s has no posture floor, so no posture runs it", g)
			continue
		}
		if _, err := ParsePosture(string(floor)); err != nil {
			t.Errorf("%s floor: %v", g, err)
		}
	}
}

// What a stricter posture quiets, every quieter one quiets too; what a quieter
// posture reviews, every stricter one reviews too.
func TestPostureRulesAreMonotone(t *testing.T) {
	t.Parallel()
	ordered := Postures()
	for i := 1; i < len(ordered); i++ {
		lower, higher := ordered[i-1].rule(), ordered[i].rule()
		quiets := []struct {
			name          string
			lower, higher bool
		}{
			{"releasesChatSecrets", lower.releasesChatSecrets, higher.releasesChatSecrets},
			{"quietsPublicRegistries", lower.quietsPublicRegistries, higher.quietsPublicRegistries},
			{"quietsOwnedLocalServices", lower.quietsOwnedLocalServices, higher.quietsOwnedLocalServices},
		}
		for _, q := range quiets {
			if q.higher && !q.lower {
				t.Errorf("%s: %s quiets what %s reviews", q.name, ordered[i], ordered[i-1])
			}
		}
		reviews := []struct {
			name          string
			lower, higher bool
		}{
			{"reviewsConfinedWrites", lower.reviewsConfinedWrites, higher.reviewsConfinedWrites},
			{"leasesAgentPolicyFiles", lower.leasesAgentPolicyFiles, higher.leasesAgentPolicyFiles},
		}
		for _, r := range reviews {
			if r.lower && !r.higher {
				t.Errorf("%s: %s reviews what %s does not", r.name, ordered[i-1], ordered[i])
			}
		}
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
