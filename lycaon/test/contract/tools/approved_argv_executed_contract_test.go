package contract

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// reportFormat is the printf format corpus stages use to echo their argv.
const reportFormat = `<%s>\n`

// The argv each process receives and every file it streams through are the
// ones the approval reviewed. Subtests share one recording gate, so they run
// serially.
func TestApprovedCommandPlanIsTheExecutedPlan(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, profiles)
	registry := tools.NewDefaultRegistry()
	background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	command := &native.CommandTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: background}
	verify := &native.VerifyTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: background}
	contractcheck.FailErr(t, "register command", registry.Register("command", command.Run))
	contractcheck.FailErr(t, "register verify", registry.Register("verify", verify.Run))
	gate := &recordingGate{}
	executor := tools.NewDefaultToolExecutor(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate), registry, "implement")

	for _, tool := range []string{"command", "verify"} {
		for _, tc := range commandPlanCorpus() {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				seedCommandTree(t, root)
				root, err := filepath.EvalSymlinks(root)
				contractcheck.FailErr(t, "resolve root", err)
				scratchDir, err := filepath.EvalSymlinks(t.TempDir())
				contractcheck.FailErr(t, "resolve scratch", err)
				before := snapshotTree(t, root)
				ctx := tools.ToolContext{
					Roots:        []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
					ActiveRootID: "root", ProjectID: "project", SourceWorkspaceKind: api.SourceWorkspaceKindProject,
					SessionID: "chat", ToolCallID: "call-" + tool, Agent: "implement",
					SessionScratchDir: scratchDir,
				}
				gate.reset()
				args := cloneArgs(tc.args).(map[string]any)
				out, err := executor.Invoke(t.Context(), tool, args, ctx)
				if err != nil {
					t.Fatalf("%s %v was refused or failed before running: %v", tool, tc.args, err)
				}
				if !argsUnchanged(tc.args, args) {
					t.Fatalf("the executor rewrote the caller's arguments: %#v, want %#v", args, tc.args)
				}
				approved, ok := gate.reviewed(tool)
				if !ok {
					t.Fatalf("%s ran without the approval gate reviewing it", tool)
				}

				want := tc.tail
				if want == "" {
					want = reportedArgv(t, approved.Args)
				}
				want = nonEmptyLines(want)
				got := nonEmptyLines(observedOutput(t, root, out, tc.output))
				if got != want {
					t.Fatalf("the process received a different argv than the approval reviewed.\n"+
						"approved: %v\nexecuted output: %q\napproved argv implies: %q\n"+
						"Rewrite arguments (glob expansion, scratch addresses, redirection binding) once, before review, and execute exactly those.",
						approved.Args, got, want)
				}

				approvedFiles := approvedStreamFiles(t, root, approved.Files, approved.ResolvedFiles)
				for _, written := range changedFiles(t, root, before) {
					if !approvedFiles[written] {
						t.Errorf("the process wrote %s, which the approval never named (files %q, resolved %q)",
							relTo(root, written), approved.Files, approved.ResolvedFiles)
					}
				}
				for _, read := range tc.reads {
					if !approvedFiles[filepath.Join(root, filepath.FromSlash(read))] {
						t.Errorf("the process read %s through a stream the approval never named (files %q)", read, approved.Files)
					}
				}
			})
		}
	}
}

// reportedArgv renders what the approved plan's reporting stages print.
func reportedArgv(t *testing.T, approved map[string]any) string {
	t.Helper()
	var stages []exec.Stage
	var err error
	if line, _ := approved["command"].(string); strings.TrimSpace(line) != "" {
		stages, err = exec.StagesFromCommandLine(line)
	} else {
		var lines []string
		raw, _ := approved["pipeline"].([]any)
		for _, item := range raw {
			s, _ := item.(string)
			lines = append(lines, s)
		}
		stages, err = exec.StagesFromCommandLines(lines)
	}
	contractcheck.FailErr(t, "parse the approved plan", err)
	var b strings.Builder
	for _, stage := range stages {
		if stage.Name != "printf" || len(stage.Args) == 0 || stage.Args[0] != reportFormat {
			continue
		}
		for _, arg := range stage.Args[1:] {
			b.WriteString("<" + arg + ">\n")
		}
	}
	return b.String()
}

// observedOutput is the report file, or the stdout chunks of the result tail.
func observedOutput(t *testing.T, root, out, file string) string {
	t.Helper()
	if file != "" {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		contractcheck.FailErr(t, "read "+file, err)
		return string(body)
	}
	var result struct {
		Tail string `json:"tail"`
	}
	contractcheck.FailErr(t, "decode result", json.Unmarshal([]byte(out), &result))
	var stdout strings.Builder
	labels := tailChunk.FindAllStringSubmatchIndex(result.Tail, -1)
	for i, label := range labels {
		end := len(result.Tail)
		if i+1 < len(labels) {
			end = labels[i+1][0]
		}
		if result.Tail[label[2]:label[3]] == "stdout" {
			stdout.WriteString(result.Tail[label[1]:end] + "\n")
		}
	}
	return stdout.String()
}

// tailChunk is the stream label that starts each output chunk of a tail.
var tailChunk = regexp.MustCompile(`(?m)^(stdout|stderr): `)

func nonEmptyLines(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// approvedStreamFiles resolves the approval's declared and resolved paths.
func approvedStreamFiles(t *testing.T, root string, declared, resolved []string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, path := range append(slices.Clone(declared), resolved...) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, filepath.FromSlash(path))
		}
		path = filepath.Clean(path)
		if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			path = filepath.Join(real, filepath.Base(path))
		}
		out[path] = true
	}
	return out
}

type fileStamp struct {
	size int64
	mod  time.Time
}

func snapshotTree(t *testing.T, root string) map[string]fileStamp {
	t.Helper()
	out := map[string]fileStamp{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fileStamp{size: info.Size(), mod: info.ModTime()}
		return nil
	})
	contractcheck.FailErr(t, "snapshot tree", err)
	return out
}

func changedFiles(t *testing.T, root string, before map[string]fileStamp) []string {
	t.Helper()
	var out []string
	for path, stamp := range snapshotTree(t, root) {
		if prior, ok := before[path]; !ok || prior != stamp {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out
}

func relTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
