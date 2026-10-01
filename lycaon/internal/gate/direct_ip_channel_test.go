package gate

import "testing"

// A direct-IP chat lease answers only the direct-network reason. Anything else
// the same action raised must still reach a card.
func TestOnlyDirectIPChannel(t *testing.T) {
	t.Parallel()
	direct := Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressDirectIP, DirectIP: true}
	detection := &Match{
		PackID: "publish-release", RuleID: "npm-publish", RuleTitle: "Publish a package",
		Level: "critical", External: true, Unrecoverable: true,
	}
	cases := []struct {
		name  string
		facts func() Facts
		want  bool
	}{
		{"direct network alone", func() Facts {
			f := baseFacts(StagePreSpawn)
			f.Containment = direct
			return f
		}, true},
		{"direct network with a detection", func() Facts {
			f := baseFacts(StagePreSpawn)
			f.Containment = direct
			f.Detection = detection
			return f
		}, false},
		{"direct network with a user ask rule", func() Facts {
			f := baseFacts(StagePreSpawn)
			f.Containment = direct
			f.UserRule = &UserRule{Category: "command", Pattern: "nc *", Subject: "nc host 22"}
			return f
		}, false},
		{"no boundary applied", func() Facts {
			f := baseFacts(StagePreSpawn)
			f.Containment = Containment{SpawnsProcess: true, Egress: EgressDirectIP, DirectIP: true}
			return f
		}, false},
		{"socket channel", func() Facts {
			f := baseFacts(StagePreSpawn)
			f.Containment = Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressProxy, SocketCount: 1, SocketPathsDigest: "d"}
			return f
		}, false},
	}
	for _, posture := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		for _, tc := range cases {
			verdict, decision := Evaluate(tc.facts(), posture)
			if verdict != Ask {
				t.Fatalf("%s at %s: verdict = %s, want ask", tc.name, posture, verdict)
			}
			if got := decision.OnlyDirectIPChannel(); got != tc.want {
				t.Fatalf("%s at %s: OnlyDirectIPChannel = %v, want %v (gates %v)", tc.name, posture, got, tc.want, decision.Gates())
			}
		}
	}
	var none *Decision
	if none.OnlyDirectIPChannel() {
		t.Fatal("a nil decision names no channel")
	}
}
