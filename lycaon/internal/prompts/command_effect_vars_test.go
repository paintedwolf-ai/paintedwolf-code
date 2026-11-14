package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func effectSet(t *testing.T, classes ...toolcontract.CommandEffectClass) toolcontract.CommandEffects {
	t.Helper()
	effects, err := toolcontract.NewCommandEffects(classes...)
	testutil.FailErr(t, "compile effects", err)
	return effects
}

func TestRestrictCommandEffectsProjectsEachEffect(t *testing.T) {
	const (
		ro  = toolcontract.CommandReadOnly
		mut = toolcontract.CommandMutation
		exe = toolcontract.CommandExternalExec
		net = toolcontract.CommandNetworkEgress
	)
	for _, tc := range []struct {
		name                    string
		offered                 []string
		effects                 []toolcontract.CommandEffectClass
		mutation, exec, network bool
	}{
		{"empty set", []string{"command"}, nil, false, false, false},
		{"read only", []string{"command"}, []toolcontract.CommandEffectClass{ro}, false, false, false},
		{"mutation", []string{"command"}, []toolcontract.CommandEffectClass{ro, mut}, true, false, false},
		{"mutation and programs", []string{"command"}, []toolcontract.CommandEffectClass{ro, mut, exe}, true, true, false},
		{"network only", []string{"command"}, []toolcontract.CommandEffectClass{ro, net}, false, false, true},
		{"everything", []string{"command"}, []toolcontract.CommandEffectClass{ro, mut, exe, net}, true, true, true},
		{"command not offered", []string{"recall"}, []toolcontract.CommandEffectClass{ro, mut, exe, net}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := prompts.VisibleToolsNativePartialVars(tc.offered)
			prompts.RestrictCommandEffects(effectSet(t, tc.effects...), tc.offered, nil, vars)
			for name, want := range map[string]bool{
				"profile_command_mutation":       tc.mutation,
				"profile_command_external_exec":  tc.exec,
				"profile_command_network_egress": tc.network,
				"profile_has_terminal_capture":   tc.exec,
			} {
				if got, ok := vars[name].(bool); !ok || got != want {
					t.Errorf("%s = %v, want %v", name, vars[name], want)
				}
			}
			facts := prompts.ExtractCommandGuidanceFacts(vars)
			for _, name := range []string{"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress"} {
				if facts[name] != vars[name] {
					t.Errorf("recorded fact %s = %v, variable %v", name, facts[name], vars[name])
				}
			}
		})
	}
}

func TestCommandEffectRestrictionsIntersect(t *testing.T) {
	offered := []string{"command"}
	writes := effectSet(t, toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	reaches := effectSet(t, toolcontract.CommandReadOnly, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	for _, order := range [][]toolcontract.CommandEffects{{writes, reaches}, {reaches, writes}, {writes, reaches, writes}} {
		vars := prompts.VisibleToolsNativePartialVars(offered)
		for _, effects := range order {
			prompts.RestrictCommandEffects(effects, offered, nil, vars)
		}
		for _, name := range []string{"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress", "profile_has_terminal_capture"} {
			if vars[name] != false {
				t.Fatalf("order %v: %s = %v after disjoint restrictions", order, name, vars[name])
			}
		}
	}
}

func TestScopeCommandEffectsFollowsDeclaredCatalogs(t *testing.T) {
	mutation := effectSet(t, toolcontract.CommandMutation)
	programs := effectSet(t, toolcontract.CommandExternalExec)
	for scope, want := range map[string][2]bool{
		"implement_investigate": {true, true},
		"decision_adjudicate":   {true, false},
		"implement_routing":     {false, false},
		"worker_readonly":       {false, false},
	} {
		got := prompts.ScopeCommandEffects(scope)
		if got.Allows(mutation) != want[0] || got.Allows(programs) != want[1] {
			t.Errorf("%s effects = %v", scope, got.Classes())
		}
	}
}
