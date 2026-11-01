package hitl

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBoundCommandsUseArgvAndPreserveArgumentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stages []toolcontract.ExecutionStage
		want   []string
	}{
		{name: "absent"},
		{name: "empty", stages: []toolcontract.ExecutionStage{}, want: []string{}},
		{name: "missing argv", stages: []toolcontract.ExecutionStage{{CommandLine: "echo fabricated"}}},
		{name: "missing executable", stages: []toolcontract.ExecutionStage{{Argv: []string{""}}}},
		{name: "reached commands", stages: []toolcontract.ExecutionStage{
			{Argv: []string{"sort", "-o", "sorted.txt", "data.txt"}, CommandLine: "echo fabricated"},
			{Argv: []string{"cat", "two words.txt"}},
		}, want: []string{"sort -o sorted.txt data.txt", `cat "two words.txt"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invocation := ActionInvocation{BoundStages: tc.stages}
			if got := invocation.BoundCommands(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("bound commands = %#v, want %#v", got, tc.want)
			}
		})
	}
}
