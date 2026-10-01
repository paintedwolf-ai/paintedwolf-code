package commandsurface_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecretInsertionPreservesCommandStructure(t *testing.T) {
	const reference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	for _, value := range []string{"with spaces", "a && additional-command", "quotes\"and'quotes", "line\nbreak", "semi;colon|pipe", "$(syntax)", "\\backslash"} {
		t.Run(value, func(t *testing.T) {
			plan, err := commandsurface.ResolvePlan(map[string]any{"command": "first --token=" + reference + " && second '" + reference + "'"}, func(s string) (string, error) { return strings.ReplaceAll(s, reference, value), nil })
			testutil.FailErr(t, "resolve command plan", err)
			stages := plan.Stages
			if len(stages) != 2 || stages[0].Name != "first" || stages[1].Name != "second" || len(stages[0].Args) != 1 || len(stages[1].Args) != 1 {
				t.Fatalf("secret changed the command structure")
			}
			if stages[0].Args[0] != "--token="+value || stages[1].Args[0] != value {
				t.Fatal("argument bytes changed")
			}
			for _, stage := range stages {
				if !strings.Contains(stage.EchoLine(), reference) {
					t.Fatal("canonical echo lost reference")
				}
			}
		})
	}
}
