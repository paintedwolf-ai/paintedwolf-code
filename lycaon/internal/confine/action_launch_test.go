package confine

import (
	"context"
	"testing"
)

func TestActionLaunchIdentityRetainsExactTypedFacts(t *testing.T) {
	for _, field := range []string{"boundary slice", "action slice", "rule pointer", "raw command", "raw boundary"} {
		t.Run(field, func(t *testing.T) {
			boundary := &Confinement{HostExecution: true, Roots: []string{"/synthetic-root"}, ProjectID: "\xff"}
			identity := EgressCommand{SessionID: "synthetic-session", CommandLine: "\xff", DeclaredHosts: []string{"fixture.invalid"}, UserRule: &EgressUserRule{Pattern: "exact"}}
			lease, err := BindAction(boundary, identity)
			if err != nil {
				t.Fatalf("bind actual action: %v", err)
			}
			defer lease.Close(context.Background())
			if err := lease.ValidateLaunch(t.Context(), boundary, identity); err != nil {
				t.Fatalf("unchanged typed action refused: %v", err)
			}
			switch field {
			case "boundary slice":
				boundary.Roots[0] = "/different-root"
			case "action slice":
				identity.DeclaredHosts[0] = "different.invalid"
			case "rule pointer":
				identity.UserRule.Pattern = "different"
			case "raw command":
				identity.CommandLine = "\xfe"
			case "raw boundary":
				boundary.ProjectID = "\xfe"
			}
			if lease.ValidateLaunch(t.Context(), boundary, identity) == nil {
				t.Fatal("changed typed action retained authority")
			}
		})
	}
}

func TestActionLaunchNoNetworkLeaseAndClosure(t *testing.T) {
	identity := EgressCommand{SessionID: "synthetic-session"}
	lease, err := BindAction(nil, identity)
	if err != nil {
		t.Fatalf("bind unmediated action: %v", err)
	}
	if err := lease.ValidateLaunch(t.Context(), nil, identity); err != nil {
		t.Fatalf("unmediated action required invented egress: %v", err)
	}
	lease.Close(context.Background())
	if lease.ValidateLaunch(t.Context(), nil, identity) == nil {
		t.Fatal("closed action retained launch authority")
	}
}
