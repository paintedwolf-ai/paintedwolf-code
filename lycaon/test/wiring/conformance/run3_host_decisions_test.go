package conformance_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/commandconformance"
)

func TestRun3StrictForeignLoopbackUsesOneChatCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local service\n")
	}))
	t.Cleanup(server.Close)
	c := commandconformance.Case{
		ID:      "run3-strict-foreign-loopback",
		Program: "curl -s '" + server.URL + "'",
	}
	runner := newConformanceRunner(t, gate.PostureStrict, false)
	obs := runner.runCaseWithRecommendedApprovals(t, c, 1)
	if obs.TopLevelReject != "" || obs.Outcome.Refusal != nil || obs.Outcome.State != "completed" || obs.Outcome.Status != 0 || obs.Outcome.Stdout != "local service\n" {
		t.Fatalf("approved command = %+v", obs)
	}
	plan := obs.ApprovalPlans[0]
	if !slices.Contains(plan.Reasons, wire.GateCapabilityWidening) {
		t.Fatalf("loopback card lacks capability gate: %+v", plan)
	}
	face := slices.IndexFunc(plan.Options, func(option wire.ApprovalOption) bool { return option.ID == plan.RecommendedOptionID })
	if face < 0 || plan.Options[face].Rung != "chat" {
		t.Fatalf("loopback recommendation does not cover this chat: %+v", plan)
	}
}

func TestRun3EvalSyntaxRefusalPreservesEarlierOutput(t *testing.T) {
	c := commandconformance.Case{ID: "run3-eval-invalid-source", Program: "printf 'before\\n'; eval 'if'"}
	runner := newConformanceRunner(t, gate.PostureLight, true)
	obs := runner.runCase(t, c)
	if obs.TopLevelReject != "" || obs.Outcome.Refusal == nil || obs.Outcome.Refusal.Code != "COMMAND_SYNTAX_INVALID" {
		t.Fatalf("eval source refusal = %+v", obs)
	}
	// A shell reports a syntax error with status 2.
	if obs.Outcome.Stdout != "before\n" || obs.Outcome.Status != 2 || obs.Outcome.Stderr == "" {
		t.Fatalf("eval refusal lost its earlier result or shell diagnostic: %+v", obs.Outcome)
	}
}

func TestRun3NativeJobPIDAndPsAux(t *testing.T) {
	c := commandconformance.Case{
		ID:      "run3-native-job-inspection",
		Program: `sleep 30 & pid=$!; printf 'pid:%s\n' "$pid"; jobs -l; ps aux | head -n 3; printf 'psstatus:%s\n' "$?"; ps -p "$pid" -o pid=; kill "$pid"; wait "$pid"; printf 'wait:%s\n' "$?"`,
	}
	runner := newConformanceRunner(t, gate.PostureLight, true)
	obs := runner.runCase(t, c)
	if obs.TopLevelReject != "" || obs.Outcome.Refusal != nil || obs.Outcome.Status != 0 {
		t.Fatalf("native job inspection = %+v", obs)
	}
	lines := strings.Split(strings.TrimSpace(obs.Outcome.Stdout), "\n")
	if len(lines) < 6 || !strings.HasPrefix(lines[0], "pid:") || lines[len(lines)-1] != "wait:143" || lines[len(lines)-3] != "psstatus:0" {
		t.Fatalf("job and ps output = %q", obs.Outcome.Stdout)
	}
	pid := strings.TrimPrefix(lines[0], "pid:")
	if n, err := strconv.Atoi(pid); err != nil || n <= 0 {
		t.Fatalf("$! is not a positive numeric job ID: %q", pid)
	}
	if fields := strings.Fields(lines[1]); len(fields) < 3 || fields[1] != pid {
		t.Fatalf("jobs -l did not report $!: %q", lines[1])
	}
	if strings.TrimSpace(lines[len(lines)-2]) != pid {
		t.Fatalf("ps -p did not report $!: %q", lines[len(lines)-2])
	}
	var header, row string
	for i := 2; i < len(lines)-3; i++ {
		if strings.Contains(lines[i], "%CPU") && strings.Contains(lines[i], "%MEM") {
			header = lines[i]
			if i+1 < len(lines)-3 {
				row = lines[i+1]
			}
			break
		}
	}
	if header == "" || row == "" {
		t.Fatalf("ps aux omitted its columns or job row: %q", obs.Outcome.Stdout)
	}
	fields := strings.Fields(row)
	if len(fields) < 10 {
		t.Fatalf("ps aux row lacks its columns: %q", row)
	}
	for _, column := range []int{2, 3, 4, 5, 7, 8, 9} {
		if fields[column] == "-" {
			t.Fatalf("ps aux left column %d blank: %q", column, row)
		}
	}
}
