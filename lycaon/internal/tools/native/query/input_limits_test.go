package query

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestQueryInputThroughputBoundary(t *testing.T) {
	for _, mode := range []struct {
		name  string
		flags []string
	}{
		{name: "values"},
		{name: "raw", flags: []string{"-R"}},
		{name: "events", flags: []string{"--stream"}},
	} {
		for _, source := range []string{"stdin", "file", "multiple-files"} {
			t.Run(mode.name+"/"+source, func(t *testing.T) {
				for _, size := range []int{4, 5} {
					input := "0   "
					if size == 5 {
						input += " "
					}
					files := map[string]string{}
					args := append([]string{"jq"}, mode.flags...)
					args = append(args, "empty")
					switch source {
					case "file":
						files["input"] = input
						input = ""
						args = append(args, "input")
					case "multiple-files":
						files["left"], files["right"] = "0 ", "0"+input[3:]
						input = ""
						args = append(args, "left", "right")
					}
					h := newHarness(t, files)
					memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
					testutil.FailErr(t, "create query memory", err)
					h.owners.Revisions.Memory = memory
					h.owners.limitOverride = map[string]int64{"stream_throughput_bytes": 4}
					result := h.exec(input, args...)
					if size == 4 {
						result.expect(t, "exact input boundary", "", 0)
					} else {
						result.refused(t, "input boundary exceeded", "COMMAND_LIMIT_EXCEEDED")
						reject := toolrejection.AsToolReject(result.err)
						if reject.Data["limit_id"] != "stream_throughput_bytes" || reject.Data["maximum"] != int64(4) || reject.Data["observed"] != int64(5) {
							t.Fatalf("input limit facts changed: %+v", reject.Data)
						}
					}
					if held := memory.InUse(); held != (toolcontract.ProgramMemoryUsage{}) {
						t.Fatalf("query input retained memory: %+v", held)
					}
				}
			})
		}
	}
}
