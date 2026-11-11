package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const (
	processFinishedEnvelopeBound = 5400
	processFinishedKickCap       = 6000
)

// A failed `./task` at the digest's size limit: the block the verification runner renders, after far more
// passing output than any delivered window holds.
func failureDigestScript(t *testing.T) (python, script string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	contractcheck.FailErr(t, "find python3", err)
	return python, filepath.Join(contractcheck.RepoRoot(t), "scripts", "verification_tests", "evidence", "failure_digest_output.py")
}

func TestFailureDigestSurvivesTheCommandResultWindow(t *testing.T) {
	python, script := failureDigestScript(t)
	for _, tc := range []struct {
		name          string
		contextTokens int
	}{
		{name: "AC-VER-01 smallest output budget", contextTokens: 32768},
		{name: "AC-VER-01 medium output budget", contextTokens: 64000},
		{name: "AC-VER-01 largest output budget", contextTokens: 200000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := programtest.New(t, programtest.Options{})
			f.Configure = func(tc2 *tools.ToolContext) { tc2.Host.ModelContextWindow = tc.contextTokens }
			source := "external -- " + python + " -B " + script + " " + f.Dir
			out, err := f.Call(t, t.Context(), source)
			contractcheck.FailErr(t, "run failing verification", err)
			var outcome command.ProgramOutcome
			contractcheck.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
			digest, err := os.ReadFile(filepath.Join(f.Dir, "digest.txt"))
			contractcheck.FailErr(t, "read rendered digest", err)
			if outcome.Status != 1 || !outcome.Truncated {
				t.Fatalf("status=%d truncated=%v; the fixture must fail and overflow its window", outcome.Status, outcome.Truncated)
			}
			if !strings.HasSuffix(outcome.Stdout, string(digest)) {
				t.Fatalf("delivered stdout lost the %d-byte digest:\n%s", len(digest), outcome.Stdout[max(0, len(outcome.Stdout)-len(digest)):])
			}
		})
	}
}

func TestFailureDigestSurvivesTheMergedTailAndProcessFinishedKick(t *testing.T) {
	python, script := failureDigestScript(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	run := exec.Command(python, "-B", script, dir)
	run.Stdout, run.Stderr = &stdout, &stderr
	var exit *exec.ExitError
	if err := run.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("fixture must exit 1: %v\n%s", err, stderr.String())
	}
	digest, err := os.ReadFile(filepath.Join(dir, "digest.txt"))
	contractcheck.FailErr(t, "read rendered digest", err)

	// The status line arrives last when its pipe is read after stdout's, the order that costs the digest most.
	ring := bgprocess.NewRingBuffer(bgprocess.DefaultRingBufferBytes)
	ring.Append("stdout", stdout.Bytes())
	ring.Append("stderr", stderr.Bytes())
	chunks, _, _ := ring.ReadSince(0)
	body := bgprocess.RenderChunks(chunks)
	if tail := bgprocess.CutTail(body, bgprocess.DefaultTailBytes); !strings.Contains(tail, string(digest)) {
		t.Fatalf("AC-VER-01: the %d-byte inline tail lost the digest", bgprocess.DefaultTailBytes)
	}

	envelope := processcontrol.CommandCompletionDigest(bgprocess.Completion{
		Handle: "proc_0123456789abcdef", OriginTool: "verify", Mode: bgprocess.JobMode("await"),
		TerminationReason: bgprocess.TerminationReason("exited"), ExitCode: 1,
		StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
		Stages: []hostcmd.StageResult{{Command: strings.Repeat("./task check-fast ", 200)}},
		Tail:   bgprocess.CutTail(body, bgprocess.DefaultRingBufferBytes),
	})
	if !strings.Contains(envelope, string(bytes.TrimRight(digest, "\n"))) {
		t.Fatalf("AC-VER-01: the completion envelope lost the digest:\n%s", envelope)
	}
	if len(envelope) > processFinishedEnvelopeBound {
		t.Fatalf("completion envelope = %d bytes, bound %d", len(envelope), processFinishedEnvelopeBound)
	}
	rendered, err := contractPersonaEngine(t).Render(context.Background(), "kicks/coordinator-process-finished.md",
		map[string]any{"command_completion": envelope})
	contractcheck.FailErr(t, "render process-finished kick", err)
	if !strings.Contains(rendered, string(bytes.TrimRight(digest, "\n"))) {
		t.Fatalf("the rendered kick lost the digest:\n%s", rendered)
	}
	if len(rendered) > processFinishedKickCap {
		t.Fatalf("rendered kick = %d bytes, cap %d", len(rendered), processFinishedKickCap)
	}
	t.Logf("digest %d bytes, envelope %d bytes, kick %d bytes\n%s", len(digest), len(envelope), len(rendered), rendered)
}
