package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// probeProgram runs one native program as the given agent and reports whether
// the host refused it for its effects or a profile deny.
func probeProgram(t *testing.T, s *replaySession, program string) bool {
	t.Helper()
	result := s.call(t, "command", map[string]any{"command": program})
	for _, code := range []string{"COMMAND_EFFECT_NOT_ALLOWED", "COMMAND_OPERATION_NOT_ALLOWED"} {
		if strings.Contains(result.Content, code) || slicesHasCode(result, code) {
			return true
		}
	}
	return false
}

func slicesHasCode(result *wire.ToolResult, code string) bool {
	for _, feedback := range result.Feedback {
		if feedback.Code == code {
			return true
		}
	}
	return false
}

// The probe below is only worth anything if the profiles disagree about something.
func TestProgramReachDistinguishesProfiles(t *testing.T) {
	s := newReplaySession(t)
	guards := s.h.Sessions.Manager.Coordinator.Guards
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
	probes := map[string]string{
		"ls": "ls .", "cat": "cat notes.txt", "grep": "grep note notes.txt", "stat": "stat notes.txt",
		"mkdir": "mkdir made", "rm": "rm notes.txt", "tee": "printf 'x' | tee made.txt", "edit": "edit --path notes.txt --old-string first --new-string changed",
		"code-rewrite": "code-rewrite --path notes.txt --pattern '$A' --rewrite '$A' --dry-run",
		"repo-map":     "repo-map",
		"summarize":    "summarize --path notes.txt",
	}
	for _, agent := range []struct{ id, profile string }{
		{"implementer", "implement"},
		{"path-explorer", "explore_readonly"},
		{"plan-writer", "plan_write_only"},
		{"plan-reviewer", "plan_review_readonly"},
	} {
		t.Run(agent.id, func(t *testing.T) {
			for program, source := range probes {
				t.Run(program, func(t *testing.T) {
					s := newReplaySession(t)
					testutil.FailErr(t, "set agent", s.h.Sessions.Manager.Chats.SetAgentType(t.Context(), s.session, agent.id))
					reach := s.h.Sessions.Manager.Coordinator.Guards.ProgramReach(t.Context(), agent.profile, "")
					if reach == nil {
						t.Fatal("no reach answer")
					}
					refused := probeProgram(t, s, source)
					if reach(program) == refused {
						t.Fatalf("reach says %v, executor refused = %v", reach(program), refused)
					}
				})
			}
		})
	}
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
	files, err := filepath.Glob(filepath.Join("..", "..", "config", "packs", "painted-wolf", "*", "host", "next-steps", "*.yaml"))
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
	fill := strings.NewReplacer("{{ paintedwolf.path }}", "notes.txt", "FILE", "notes.txt", "PATH", "notes.txt", "SYMBOL", "note", "A,Bp", "1,2p")
	seen := map[string]bool{}
	for _, words := range programs {
		// Signaling needs a live job, and nothing here should be signaled.
		if words[0] == "kill" {
			continue
		}
		source := fill.Replace(strings.Join(words, " "))
		if strings.HasPrefix(source, "sed -n 1,2p") {
			source = "sed -n '1,2p' notes.txt"
		}
		if seen[source] {
			continue
		}
		seen[source] = true
		t.Run(source, func(t *testing.T) {
			s := newReplaySession(t)
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
}
