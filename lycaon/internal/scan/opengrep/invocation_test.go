package opengrep

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInvocationUsesCallerBudgetForEveryAnalysisMode(t *testing.T) {
	for _, mode := range []Mode{Intraprocedural, Intrafile} {
		t.Run(string(mode), func(t *testing.T) {
			invocation := Invocation{Analysis: Analysis{Mode: mode}, Output: "/output.json", Rules: []string{"/rules.yaml"}, Targets: []string{"/source"}, Jobs: 2}
			args, err := invocation.Args()
			testutil.FailErr(t, "construct bounded invocation", err)
			separator := slices.Index(args, "--")
			for _, policy := range []string{"--timeout", "--max-target-bytes"} {
				count := 0
				for index, arg := range args {
					if arg == policy {
						count++
						if index+1 >= len(args) || args[index+1] != "0" {
							t.Fatalf("engine %s cuts host coverage: %v", policy, args)
						}
					}
				}
				if count != 1 || separator < 0 || slices.Index(args, policy) > separator {
					t.Fatalf("expected one %s policy before target arguments: %v", policy, args)
				}
			}
		})
	}
}
