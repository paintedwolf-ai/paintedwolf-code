package commands

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// programProbe asks the executor one question about a native program. Effect
// reports whether the program did what it was asked, read from the outcome or
// from the directory the probe acts in.
type programProbe struct {
	// source names the agent's write directory as {w}; a probe that names it
	// acts there, so a refusal is about the program, never about the path.
	program, source string
	// seed names the files the probe works on, so a mutating probe never
	// touches what a later probe reads.
	seed   map[string]string
	effect func(dir string, out command.ProgramOutcome) bool
}

const probeNotes = "first note\nsecond note\n"

// programProbes lists the read-only probes first; each mutating probe works on
// its own file.
var programProbes = []programProbe{
	{program: "ls", source: "ls .", effect: stdoutHas("notes.txt")},
	{program: "cat", source: "cat notes.txt", effect: stdoutIs(probeNotes)},
	{program: "grep", source: "grep note notes.txt", effect: stdoutIs(probeNotes)},
	{program: "stat", source: "stat notes.txt", effect: stdoutHas("notes.txt")},
	{program: "code-rewrite", source: "code-rewrite --path notes.txt --pattern '$A' --rewrite '$A' --dry-run",
		effect: func(dir string, out command.ProgramOutcome) bool {
			return out.Status == 0 && fileIs(dir, "notes.txt", probeNotes)
		}},
	{program: "repo-map", source: "repo-map", effect: stdoutHas("notes.txt")},
	{program: "summarize", source: "summarize --path notes.txt", effect: func(_ string, out command.ProgramOutcome) bool {
		return out.Status == 0 && strings.TrimSpace(out.Stdout) != ""
	}},
	{program: "mkdir", source: "mkdir {w}/made", effect: func(dir string, _ command.ProgramOutcome) bool {
		info, err := os.Stat(filepath.Join(dir, "made"))
		return err == nil && info.IsDir()
	}},
	{program: "rm", source: "rm {w}/rm-probe.txt", seed: map[string]string{"rm-probe.txt": "remove me\n"},
		effect: func(dir string, _ command.ProgramOutcome) bool {
			_, err := os.Stat(filepath.Join(dir, "rm-probe.txt"))
			return errors.Is(err, fs.ErrNotExist)
		}},
	{program: "tee", source: "printf 'x' | tee {w}/tee-probe.txt", effect: func(dir string, _ command.ProgramOutcome) bool {
		return fileIs(dir, "tee-probe.txt", "x")
	}},
	{program: "edit", source: "edit --path {w}/edit-probe.txt --old-string first --new-string changed",
		seed: map[string]string{"edit-probe.txt": "first note\n"},
		effect: func(dir string, _ command.ProgramOutcome) bool {
			return fileIs(dir, "edit-probe.txt", "changed note\n")
		}},
}

func stdoutIs(want string) func(string, command.ProgramOutcome) bool {
	return func(_ string, out command.ProgramOutcome) bool { return out.Status == 0 && out.Stdout == want }
}

func stdoutHas(want string) func(string, command.ProgramOutcome) bool {
	return func(_ string, out command.ProgramOutcome) bool {
		return out.Status == 0 && strings.Contains(out.Stdout, want)
	}
}

func fileIs(dir, name, want string) bool {
	data, err := os.ReadFile(filepath.Join(dir, name))
	return err == nil && string(data) == want
}

// profileRefusals are the typed answers for a program the agent may not run.
var profileRefusals = []string{"COMMAND_EFFECT_NOT_ALLOWED", "COMMAND_OPERATION_NOT_ALLOWED"}

// probeProgram runs one probe and reports whether the host refused it. A run
// must show its effect and a refusal must be typed and leave the world as it
// was; anything else fails the test.
func probeProgram(t *testing.T, s *replaySession, probe programProbe, writeDir string) (refused bool) {
	t.Helper()
	dir := s.dir
	if strings.Contains(probe.source, "{w}") {
		dir = filepath.Join(s.dir, writeDir)
		testutil.FailErr(t, "create write directory", os.MkdirAll(dir, 0o755))
	}
	for name, body := range probe.seed {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	source := strings.ReplaceAll(probe.source, "{w}", filepath.ToSlash(writeDir))
	result := s.call(t, "command", map[string]any{"command": source})
	out, hasOutcome := probeOutcome(t, result)
	typed := slices.ContainsFunc(result.Feedback, func(f wire.ToolFeedback) bool { return slices.Contains(profileRefusals, f.Code) }) ||
		(hasOutcome && out.Refusal != nil && slices.Contains(profileRefusals, out.Refusal.Code))
	switch {
	case typed:
		if hasOutcome && probe.effect(dir, out) {
			t.Fatalf("%s was refused but its effect happened: %+v", source, out)
		}
		return true
	case hasOutcome && out.State == "completed" && out.Refusal == nil && probe.effect(dir, out):
		return false
	default:
		t.Fatalf("%s neither ran with its effect nor was refused by profile: outcome %+v, feedback %+v", source, out, result.Feedback)
		return false
	}
}

// probeOutcome decodes the program outcome a command result carries.
func probeOutcome(t *testing.T, result *wire.ToolResult) (command.ProgramOutcome, bool) {
	t.Helper()
	_, body, _, ok := hostmarker.SplitToolJSONBody(result.Content)
	if !ok {
		return command.ProgramOutcome{}, false
	}
	var out command.ProgramOutcome
	if json.Unmarshal([]byte(body), &out) != nil || out.ProgramID == "" {
		return command.ProgramOutcome{}, false
	}
	return out, true
}

// The probe below is only worth anything if the profiles disagree about something.
func TestProgramReachDistinguishesProfiles(t *testing.T) {
	guards := newProgramHost(t).h.Sessions.Manager.Coordinator.Guards
	implement, readonly := guards.ProgramReach(t.Context(), "implement", ""), guards.ProgramReach(t.Context(), "explore_readonly", "")
	if !implement("rm") || readonly("rm") || !readonly("cat") {
		t.Fatalf("rm implement=%v readonly=%v, cat readonly=%v", implement("rm"), readonly("rm"), readonly("cat"))
	}
	surface := guards.ProgramReach(t.Context(), "coordinator", "observe_plan")
	if surface("rm") || !surface("cat") {
		t.Fatalf("read-only surface: rm=%v cat=%v", surface("rm"), surface("cat"))
	}
}

// What reach says about a native program is what the executor then does with it.
func TestProgramReachMatchesTheExecutor(t *testing.T) {
	// writeDir is inside each profile's write scope.
	agents := []struct{ id, profile, writeDir string }{
		{"implementer", "implement", "."},
		{"path-explorer", "explore_readonly", "."},
		{"plan-writer", "plan_write_only", filepath.Join(settingsoverlay.DirName(), "blueprints")},
		{"plan-reviewer", "plan_review_readonly", "."},
	}
	p := newProgramHost(t)
	asserted := 0
	for _, agent := range agents {
		t.Run(agent.id, func(t *testing.T) {
			s := p.replay(t)
			testutil.FailErr(t, "set agent", p.h.Sessions.Manager.Chats.SetAgentType(t.Context(), s.session, agent.id))
			reach := p.h.Sessions.Manager.Coordinator.Guards.ProgramReach(t.Context(), agent.profile, "")
			if reach == nil {
				t.Fatal("no reach answer")
			}
			for _, probe := range programProbes {
				t.Run(probe.program, func(t *testing.T) {
					if refused := probeProgram(t, s, probe, agent.writeDir); reach(probe.program) == refused {
						t.Fatalf("reach says %v, executor refused = %v", reach(probe.program), refused)
					}
					asserted++
				})
			}
		})
	}
	if want := len(agents) * len(programProbes); asserted != want {
		t.Fatalf("asserted %d of %d agent and program cells", asserted, want)
	}
	t.Logf("asserted %d agent and program cells on one host", asserted)
}

// A retired tool's answer names native forms; each must be a real program.
func TestRetiredToolFormsAreRealPrograms(t *testing.T) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "load language", err)
	known := map[string]bool{}
	for _, spec := range catalog.Commands {
		known[spec.Name] = true
		for _, alias := range spec.Aliases {
			known[alias] = true
		}
	}
	for _, disposition := range catalog.Tools {
		for _, form := range disposition.CommandForms {
			if !known[form] {
				t.Errorf("%s names native form %q, which the language does not declare", disposition.Tool, form)
			}
		}
	}
}

func collectStepPrograms(steps []oar.NextStep, out *[][]string) {
	for _, step := range steps {
		if len(step.Program) > 0 {
			*out = append(*out, step.Program)
		}
		collectStepPrograms(step.Or, out)
	}
}

// Every native program a next step offers is accepted as written by the real
// language, once its placeholders are filled.
func TestOfferedProgramsParseAndRun(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "*", "host", "next-steps", "*.yaml"))
	testutil.FailErr(t, "list catalog", err)
	if len(files) == 0 {
		t.Fatal("no next-step catalog found")
	}
	var programs [][]string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		testutil.FailErr(t, "read "+file, err)
		entry, err := oar.DecodeNextSteps(raw)
		testutil.FailErr(t, "decode "+file, err)
		collectStepPrograms(entry.Steps, &programs)
	}
	fill := strings.NewReplacer("{{ paintedwolf.path }}", "notes.txt", "{{ paintedwolf.spill_path }}", "notes.txt", "FILE", "notes.txt", "PATH", "notes.txt", "SYMBOL", "note", "A,Bp", "1,2p")
	p := newProgramHost(t)
	seen := map[string]bool{}
	for _, words := range programs {
		// Signaling needs a live job, and nothing here should be signaled.
		if words[0] == "kill" {
			continue
		}
		source := fill.Replace(strings.Join(words, " "))
		if seen[source] {
			continue
		}
		seen[source] = true
		t.Run(source, func(t *testing.T) {
			s := p.replay(t)
			result := s.call(t, "command", map[string]any{"command": source})
			if result.Outcome == wire.ToolResultOutcomeRejected {
				t.Fatalf("refused: %+v", result.Feedback)
			}
			if strings.Contains(result.Content, `"status":2`) {
				t.Fatalf("usage error: %.400s", result.Content)
			}
		})
	}
	if len(seen) == 0 {
		t.Fatal("no programs probed")
	}
	t.Logf("ran %d offered programs on one host", len(seen))
}
