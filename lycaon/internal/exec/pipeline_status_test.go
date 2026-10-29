package exec

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPipelineUsesRightmostFailureEvenWhenFinalStageSucceeds(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate status fixture process", err)
	stages := make([]Stage, 0, 3)
	for _, status := range []int{3, 7, 0} {
		stages = append(stages, Stage{Name: self, Args: []string{"-test.run=^TestPipelineStatusFixtureProcess$", "--", "fixture-status=" + strconv.Itoa(status)}})
	}
	result, err := RunPipeline(context.Background(), stages, ExecOpts{Launch: HostLaunch("pipeline status fixture"), Dir: t.TempDir(), Timeout: 30 * time.Second, MaxOutputBytes: 1024})
	testutil.FailErr(t, "run distinct-failure pipeline", err)
	if result.ExitCode != 7 || string(result.Output) != "fixture" {
		t.Fatalf("final success concealed or reordered earlier failure: %+v output=%q", result.Stages, result.Output)
	}
	for i, want := range []int{3, 7, 0} {
		if result.Stages[i].ExitStatus() != want || result.Stages[i].Failed != (want != 0) {
			t.Fatalf("stage %d lost actual status: %+v", i, result.Stages[i])
		}
	}
}

// TestPipelineStatusFixtureProcess is an actual child with synthetic fixture
// output/status. The ordinary test invocation performs no subprocess work here.
func TestPipelineStatusFixtureProcess(t *testing.T) {
	for _, arg := range os.Args {
		if !strings.HasPrefix(arg, "fixture-status=") {
			continue
		}
		status, err := strconv.Atoi(strings.TrimPrefix(arg, "fixture-status="))
		if err != nil {
			os.Exit(125)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(124)
		}
		if _, err := io.WriteString(os.Stdout, "fixture"); err != nil {
			os.Exit(123)
		}
		os.Exit(status)
	}
}
