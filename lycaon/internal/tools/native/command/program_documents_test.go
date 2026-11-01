package command

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestProgramDocumentNoticeAdmissionSurvivesCompletionTransfer(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	testutil.FailErr(t, "memory", err)
	documents, err := newProgramDocuments(t.Context(), tools.ToolContext{}, memory)
	testutil.FailErr(t, "notice owner", err)
	testutil.FailErr(t, "first notice", documents.note(t.Context(), "root", "source.txt"))
	testutil.FailErr(t, "coalesced notice", documents.note(t.Context(), "root", "source.txt"))
	paths, release := documents.takePaths()
	if len(paths) != 1 || paths[0] != "source.txt" || release == nil {
		t.Fatalf("paths=%v release=%v", paths, release != nil)
	}
	before := memory.InUse().State
	documents.close()
	if memory.InUse().State != before || before == 0 {
		t.Fatalf("transferred admission released early: before=%d now=%+v", before, memory.InUse())
	}
	release()
	release()
	if memory.InUse().State != 0 {
		t.Fatalf("completion admission leaked: %+v", memory.InUse())
	}
}

func TestProgramDocumentInitialNotesUseHeldResultEnvelope(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	testutil.FailErr(t, "memory", err)
	documents, err := newProgramDocuments(t.Context(), tools.ToolContext{}, memory)
	testutil.FailErr(t, "notice owner", err)
	testutil.FailErr(t, "notice", documents.note(t.Context(), "root", "source.txt"))
	used := memory.InUse().State
	stderr := strings.Repeat("e", CommandOutputBudget(0))
	joined := documents.appendNotes(stderr)
	if len(joined) <= len(stderr) || memory.InUse().State != used || used < int64(len(joined)) {
		t.Fatalf("unadmitted result: bytes=%d admission=%+v", len(joined), memory.InUse())
	}
	paths, release := documents.takePaths()
	if len(paths) != 0 || release != nil {
		t.Fatalf("initial notice transferred again: %v", paths)
	}
	documents.close()
	if memory.InUse().State != 0 {
		t.Fatalf("initial admission leaked: %+v", memory.InUse())
	}
}

func TestProgramDocumentNoticeRefusesBeforeRetainingUnadmittedEntry(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: int64(CommandOutputBudget(0)) + 1024})
	testutil.FailErr(t, "memory", err)
	documents, err := newProgramDocuments(t.Context(), tools.ToolContext{}, memory)
	testutil.FailErr(t, "notice owner", err)
	err = documents.note(t.Context(), "root", "source.txt")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" {
		t.Fatalf("limit=%v", err)
	}
	if len(documents.notes) != 0 || memory.InUse().State != int64(CommandOutputBudget(0)) {
		t.Fatalf("unadmitted entry or lease: notes=%v memory=%+v", documents.notes, memory.InUse())
	}
	documents.close()
	if memory.InUse().State != 0 {
		t.Fatalf("refused admission leaked: %+v", memory.InUse())
	}
}

func TestControlDefinitionsNamespaceTakenToolIdentities(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "language catalog", err)
	run := &programRun{
		catalog:  catalog,
		commands: commandIndex(catalog),
		control: programControlDefinition{
			available: map[string]bool{"sed": true, "git": true},
			taken:     map[string]bool{"read": true, "git": true},
		},
	}
	definitions, err := run.controlDefinitions()
	testutil.FailErr(t, "control definitions", err)
	if got := definitions["sed"].Facts().Identity; got != "sed" {
		t.Fatalf("sed identity = %q, want sed", got)
	}
	if got := definitions["read"].Facts().Identity; got != "command-host:read" {
		t.Fatalf("read identity = %q, want command-host:read", got)
	}
	if got := definitions["git"].Facts().Identity; got != "command-host:git" {
		t.Fatalf("git identity = %q, want command-host:git", got)
	}
}
