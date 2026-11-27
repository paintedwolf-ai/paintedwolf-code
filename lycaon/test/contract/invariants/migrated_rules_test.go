package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

func TestSourceRecorderAssertionRule(t *testing.T) {
	t.Parallel()
	check := rule(t, forbiddencalls.Family, "source-recording-port-recovers-no-peer-capabilities")
	for _, tc := range []struct {
		name, body string
		want       outcome
	}{
		{"direct", "_ = tc.Source.SourceLedger.(History)", violates},
		{"alias", "ledger := tc.Source.SourceLedger; reader := ledger; _ = reader.(History)", violates},
		{"type switch", "switch tc.Source.SourceLedger.(type) { case History: }", violates},
		{"explicit port", "_ = tc.Source.History.Files.(History)", passes},
		{"unrelated recorder", "_ = events.Recorder.(Events)", passes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"lycaon/internal/fixture/source.go": "package fixture\nfunc check() { _ = tc.Source.SourceLedger; " + tc.body + " }",
			}
			expect(t, invariants.Evaluate(fixture(t, files), check), tc.want, 1)
		})
	}
}

func TestBoundedTimeoutClaimRule(t *testing.T) {
	t.Parallel()
	check := rule(t, forbiddencalls.Family, "bounded-timeout-claims-construct-a-deadline")
	files := map[string]string{
		"lycaon/internal/project/project.go": `package project
import deadline "context"
func gitRemoteHash() { deadline.WithTimeout(nil, 0) }
`,
		"lycaon/internal/worker/queue.go": `package worker
import "context"
type SQLQueue struct{}
func (SQLQueue) Get() { context.WithDeadline(nil, deadline) }
`,
		"lycaon/internal/llm/discovery.go": `package llm
import "context"
func fetchDiscoveredModels() { context.WithTimeout(nil, 0) }
`,
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), passes, 0)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		"lycaon/internal/project/project.go": `package project; func gitRemoteHash() { other.WithTimeout(nil, 0) }`,
	})), check), violates, 1)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		"lycaon/internal/project/project.go": `package project; func renamed() {}`,
	})), check), missesAnchor, 0)
}

func TestCommandNativeImportRule(t *testing.T) {
	t.Parallel()
	check := rule(t, forbiddencalls.Family, "command-imports-only-declared-native-seams")
	files := map[string]string{
		"lycaon/internal/tools/native/command/command.go": `package command
import (
	"strings"
	_ "github.com/lycaon/lycaon/internal/tools/native/terminal"
	_ "github.com/lycaon/lycaon/internal/tools/native/toolkit"
)
`,
		"lycaon/internal/tools/native/command/tooltest/fixture.go": `package tooltest
import _ "github.com/lycaon/lycaon/internal/tools/native"
`,
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), passes, 0)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		"lycaon/internal/tools/native/command/command_test.go": `package command_test
import (
	"strings"
	_ "github.com/lycaon/lycaon/internal/tools/native/command"
	_ "github.com/lycaon/lycaon/internal/tools/native"
	_ "github.com/lycaon/lycaon/internal/tools/native/survey"
)
`,
	})), check), violates, 2)
}

func TestGitDoorChecksPackageInitializers(t *testing.T) {
	t.Parallel()
	check := rule(t, forbiddencalls.Family, "git-one-door")
	files := map[string]string{
		"lycaon/internal/gitexec/git.go": "package gitexec",
		"lycaon/internal/extpacks/git.go": `package extpacks
import "os/exec"
var cmd = exec.Command("git-lfs", "version")
func clone() { exec.CommandContext(nil, "git", "version") }
`,
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), violates, 2)
}

func TestUtilityLaneRule(t *testing.T) {
	t.Parallel()
	check := rule(t, forbiddencalls.Family, "curation-stays-on-the-admitted-turn")
	const (
		review = "lycaon/internal/workflow/review/review.go"
		turn   = "lycaon/internal/session/turnexecution/run.go"
		llm    = "lycaon/internal/llm/utility.go"
	)
	files := map[string]string{
		review: `package review
func StampFanoutExecuteOutput() {}
func (ReviewEvidence) StampEvidence() {}
func compileReviewLoopDigest() {}
`,
		"lycaon/internal/session/workeroutcomes/results.go": `package workeroutcomes
func (Results) EnvelopeForTerminal() {}
`,
		turn: `package turnexecution
func (s *Service) Run(ctx any) {
	s.Turns.Begin(ctx)
	ctx = curation.WithSession(ctx)
	ctx = curation.WithLane(ctx)
	s.executePromptRun(ctx)
}
`,
		llm: `package llm
func (r *RegistrySummarizer) streamLite() { r.beginLaneCall(); r.endLaneCall() }
func (r *RegistrySummarizer) completeUtility() { r.beginLaneCall(); defer r.endLaneCall() }
`,
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), passes, 0)
	for _, tc := range []struct{ name, file, before, after string }{
		{"curation in review stamp", review, "StampEvidence() {}", "StampEvidence() { curator.Curate() }"},
		{"wrong context", turn, "s.executePromptRun(ctx)", "s.executePromptRun(other)"},
		{"out of order", turn, "ctx = curation.WithSession(ctx)\n\tctx = curation.WithLane(ctx)", "ctx = curation.WithLane(ctx)\n\tctx = curation.WithSession(ctx)"},
		{"mentions lane end without calling it", llm, "r.beginLaneCall(); r.endLaneCall()", "r.beginLaneCall(); _ = r.endLaneCall"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(files[tc.file], tc.before, tc.after, 1)
			if changed == files[tc.file] {
				t.Fatalf("fixture did not contain %q", tc.before)
			}
			expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{tc.file: changed})), check), violates, 1)
		})
	}
}

func TestRawSignalsRule(t *testing.T) {
	t.Parallel()
	check := bare(rule(t, forbiddencalls.Family, "raw-process-signals-stay-in-platform-adapters"))
	check.Exempt = []invariants.Exemption{
		{Key: "internal/osprocess", Reason: "owns platform signaling"},
	}
	const config = "lycaon/.golangci.yml"
	files := map[string]string{
		config: `linters:
  enable: [forbidigo]
  settings:
    forbidigo:
      forbid:
        - pattern: ^syscall\.Kill$
        - pattern: ^(golang\.org/x/sys/)?unix\.(Kill|Killpg|PidfdSendSignal)$
        - pattern: ^os\.FindProcess$
`,
		"lycaon/internal/osprocess/signal_linux.go": `package osprocess
import "syscall"
func Signal() { syscall.Kill(1, 0) }
`,
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), passes, 0)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		"lycaon/cmd/app/main_linux.go": `package main
import (
	sys "syscall"
	"golang.org/x/sys/unix"
	"os"
)
func main() {
	sys.Kill(1, 0)
	unix.Killpg(1, 0)
	unix.PidfdSendSignal(1, 0, nil, 0)
	os.FindProcess(1)
}
`,
	})), check), violates, 4)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		config: strings.ReplaceAll(files[config], `- pattern: ^os\.FindProcess$`, `# - pattern: ^os\.FindProcess$`),
	})), check), violates, 1)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		"lycaon/internal/osprocess/signal_linux.go": "package osprocess; func Signal() {}",
	})), check), staleExemption, 0)
}
