package inject

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildScopeFileOrientationsSingleFile(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x\n", 500) + "class Big:\n    pass\n"
	path := filepath.Join(dir, "big.py")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(body), 0o644))

	out, err := BuildScopeFileOrientations(context.Background(), dir, api.TaskScope{
		Mode:  api.TaskScopeModeWrite,
		Paths: []string{"big.py"},
	})
	testutil.FailErr(t, "BuildScopeFileOrientations", err)
	if len(out) != 1 || out[0].Path != "big.py" {
		t.Fatalf("out = %+v", out)
	}
	if out[0].TotalLines < 500 {
		t.Fatalf("TotalLines = %d", out[0].TotalLines)
	}
}

func TestRenderWorkerTaskAssignmentIncludesFileOrientation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	body := "class Game:\n    pass\n\ndef tick():\n    pass\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "game.py"), []byte(body), 0o644))

	out, err := RenderWorkerTaskAssignment(ctx, testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:    "sess-inject-test",
		ProjectDir:   dir,
		Charter:      testWorkerCharter("Tune velocity integration in game.py"),
		AgentType:    "implementer",
		WorkerJobID:  "job-game-1",
		Scope:        api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py"}},
		MaxToolLoops: 8,
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	if !strings.Contains(out, "File orientation") {
		t.Fatalf("missing orientation block: %q", out)
	}
	if !strings.Contains(out, "game.py") {
		t.Fatalf("missing path in %q", out)
	}
	if !strings.Contains(out, "structural hints") {
		t.Fatalf("missing file-orientation survey rule in %q", out)
	}
}

// symbolRichJSON writes a JSON object with n keys — a lockfile's outline shape.
func symbolRichJSON(t *testing.T, dir, name string, n int) {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n")
	for i := range n {
		fmt.Fprintf(&b, "  \"dependency_%04d\": {\"version\": \"1.0.%d\"},\n", i, i)
	}
	b.WriteString("  \"last\": true\n}\n")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644))
}

// An outline past the render ceiling must still render, and say what it dropped.
func TestRenderWorkerTaskAssignmentSurvivesSymbolRichScopeFile(t *testing.T) {
	dir := t.TempDir()
	symbolRichJSON(t, dir, "package-lock.json", renderCeiling*4)

	orients, err := BuildScopeFileOrientations(t.Context(), dir, api.TaskScope{
		Mode: api.TaskScopeModeRead, Paths: []string{"package-lock.json"},
	})
	testutil.FailErr(t, "BuildScopeFileOrientations", err)
	if len(orients) != 1 {
		t.Fatalf("orientations = %d, want 1", len(orients))
	}
	if got := len(orients[0].Symbols); got != MaxScopeOutlineSymbols {
		t.Fatalf("Symbols = %d, want the budget %d", got, MaxScopeOutlineSymbols)
	}
	if orients[0].SymbolsElided <= 0 {
		t.Fatal("SymbolsElided must count what the budget dropped")
	}

	out, err := RenderWorkerTaskAssignment(t.Context(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:    "sess-bounded",
		ProjectDir:   dir,
		Charter:      testWorkerCharter("Audit the lockfile"),
		AgentType:    "security-reviewer",
		WorkerJobID:  "job-bounded-1",
		Scope:        api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"package-lock.json"}},
		MaxToolLoops: 60,
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	if !strings.Contains(out, "more not listed") {
		t.Fatalf("the inline outline must say it was shortened: %q", out)
	}
	if !strings.Contains(out, "Shortened for this block") {
		t.Fatalf("the block must carry an elision note: %q", out)
	}
	if !strings.Contains(out, "outline symbols for `package-lock.json`") {
		t.Fatalf("the elision note must name the file: %q", out)
	}
}

// Model-authored lists are bounded on the same contract as the outline.
func TestBuildWorkerTaskAssignmentDataBoundsModelAuthoredLists(t *testing.T) {
	paths := make([]string, MaxScopePaths+7)
	for i := range paths {
		paths[i] = fmt.Sprintf("src/pkg%03d", i)
	}
	facts := make([]string, MaxCharterItems+3)
	for i := range facts {
		facts[i] = fmt.Sprintf("fact %d", i)
	}
	data := BuildWorkerTaskAssignmentData(t.Context(), WorkerTaskAssignmentInput{
		SessionID:   "sess-bounds",
		ProjectDir:  t.TempDir(),
		Charter:     api.WorkerTaskCharter{Goal: "g", DoneWhen: []string{"d"}, KnownFacts: facts},
		AgentType:   "explorer",
		WorkerJobID: "job-bounds",
		Scope:       api.TaskScope{Mode: api.TaskScopeModeRead, Paths: paths},
	})
	if len(data.ScopePaths) != MaxScopePaths {
		t.Fatalf("ScopePaths = %d, want %d", len(data.ScopePaths), MaxScopePaths)
	}
	if len(data.KnownFacts) != MaxCharterItems {
		t.Fatalf("KnownFacts = %d, want %d", len(data.KnownFacts), MaxCharterItems)
	}
	if len(data.Elisions) != 2 {
		t.Fatalf("Elisions = %v, want one note per shortened list", data.Elisions)
	}
}

// Every budget must stay under the ceiling it exists to avoid.
func TestRenderBudgetsStayUnderTheRenderCeiling(t *testing.T) {
	for name, budget := range renderBudgets() {
		if budget <= 0 {
			t.Fatalf("%s = %d: a budget must bound something", name, budget)
		}
		if budget >= renderCeiling {
			t.Fatalf("%s = %d exceeds the render ceiling %d — the template would error, not truncate",
				name, budget, renderCeiling)
		}
	}
}

// The leg must be able to read the record its brief names.
func TestRenderWorkerTaskAssignmentCarriesRecordedVerdicts(t *testing.T) {
	out, err := RenderWorkerTaskAssignment(t.Context(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:   "sess-verdicts",
		ProjectDir:  t.TempDir(),
		Charter:     testWorkerCharter("Challenge the claims recorded under evidence_key survey_claims"),
		AgentType:   "skeptic",
		WorkerJobID: "job-skeptic-1",
		Scope:       api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"src"}},
		RecordedVerdicts: []RecordedVerdict{{
			Phase:       "claims",
			EvidenceKey: "survey_claims",
			Fields: []RecordedVerdictField{
				{Name: "verdict", Value: "CLAIMED"},
				{Name: "claims", Value: `[{"id":"C1","statement":"loader imports arbitrary modules"}]`},
			},
		}},
		MaxToolLoops: 60,
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	for _, want := range []string{"survey_claims", "CLAIMED", "loader imports arbitrary modules", "phase `claims`", "there is no tool to look one up"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// An early-phase leg has no stamped verdict; the block stays out entirely.
func TestRenderWorkerTaskAssignmentOmitsTheVerdictBlockWhenNoneRecorded(t *testing.T) {
	out, err := RenderWorkerTaskAssignment(t.Context(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:    "sess-no-verdicts",
		ProjectDir:   t.TempDir(),
		Charter:      testWorkerCharter("Survey the attack surface"),
		AgentType:    "security-reviewer",
		WorkerJobID:  "job-survey-1",
		Scope:        api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"src"}},
		MaxToolLoops: 60,
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	if strings.Contains(out, "Recorded by this run") {
		t.Fatalf("the block must be absent with nothing stamped:\n%s", out)
	}
}

// Carried verdicts are bounded like every other rendered list.
func TestBuildWorkerTaskAssignmentDataBoundsRecordedVerdicts(t *testing.T) {
	verdicts := make([]RecordedVerdict, MaxRecordedVerdicts+3)
	for i := range verdicts {
		fields := make([]RecordedVerdictField, MaxRecordedVerdictFields+2)
		for j := range fields {
			fields[j] = RecordedVerdictField{Name: fmt.Sprintf("f%d", j), Value: "v"}
		}
		verdicts[i] = RecordedVerdict{Phase: "p", EvidenceKey: fmt.Sprintf("key%d", i), Fields: fields}
	}
	data := BuildWorkerTaskAssignmentData(t.Context(), WorkerTaskAssignmentInput{
		SessionID:        "sess-verdict-bounds",
		ProjectDir:       t.TempDir(),
		Charter:          api.WorkerTaskCharter{Goal: "g", DoneWhen: []string{"d"}},
		AgentType:        "skeptic",
		WorkerJobID:      "job-bounds",
		RecordedVerdicts: verdicts,
	})
	if len(data.RecordedVerdicts) != MaxRecordedVerdicts {
		t.Fatalf("RecordedVerdicts = %d, want %d", len(data.RecordedVerdicts), MaxRecordedVerdicts)
	}
	for _, v := range data.RecordedVerdicts {
		if len(v.Fields) != MaxRecordedVerdictFields {
			t.Fatalf("verdict %s fields = %d, want %d", v.EvidenceKey, len(v.Fields), MaxRecordedVerdictFields)
		}
	}
	if len(data.Elisions) != 1+MaxRecordedVerdicts {
		t.Fatalf("Elisions = %d, want one for the list plus one per kept verdict: %v", len(data.Elisions), data.Elisions)
	}
}
