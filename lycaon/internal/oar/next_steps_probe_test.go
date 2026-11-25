package oar

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func TestResolveNextStepsChecksAProgramByItsProbe(t *testing.T) {
	shown := []string{"sed", "-n", "A,Bp", "FILE"}
	request := toolreach.Offered([]string{"command"}).WithPrograms(allPrograms)
	for _, tc := range []struct {
		name  string
		probe []string
		calls int
	}{
		{"placeholder words alone do not resolve", nil, 0},
		{"probe resolves the invocation", []string{"sed", "-n", "1,2p", "FILE"}, 1},
		{"a probe outside the program's admitted modes", []string{"sed", "-i", "s/a/b/", "FILE"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := stepRule(NextStep{Program: shown, Probe: tc.probe, Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"})
			got := ResolveNextSteps(t.Context(), rule, reachContext(request))
			if len(got.Calls) != tc.calls {
				t.Fatalf("calls = %+v, want %d", got.Calls, tc.calls)
			}
			if tc.calls == 1 && got.Calls[0].Args["command"] != "sed -n A,Bp FILE" {
				t.Fatalf("shown call = %v, want the declared words", got.Calls[0].Args)
			}
		})
	}
}

func TestValidateNextStepsKeepsAProbeOnItsProgram(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	for _, step := range []NextStep{
		{Program: []string{"sed", "-n", "A,Bp", "FILE"}, Probe: []string{"cat", "FILE"}, Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"},
		{Tool: "recall", Probe: []string{"sed", "-n", "1,2p"}, Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"},
	} {
		if err := loader.validateNextSteps(stepRule(step)); err == nil {
			t.Errorf("step %+v validated", step)
		}
	}
	valid := NextStep{Program: []string{"sed", "-n", "A,Bp", "FILE"}, Probe: []string{"sed", "-n", "1,2p", "FILE"}, Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"}
	testutil.FailErr(t, "validate probe", loader.validateNextSteps(stepRule(valid)))
}
