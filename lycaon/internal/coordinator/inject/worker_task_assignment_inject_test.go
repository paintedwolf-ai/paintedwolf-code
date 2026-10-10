package inject

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func testInjectRenderer(t *testing.T) *prompts.InjectRenderer {
	t.Helper()
	return prompts.NewInjectRenderer(testPromptEngine(t))
}

func testWorkerCharter(goal string) api.WorkerTaskCharter {
	return api.WorkerTaskCharter{Goal: goal, DoneWhen: []string{"Return grounded results."}}
}

func TestWorkerAssignmentCarriesInventoryIndependentlyOfTheBrief(t *testing.T) {
	inventory := `{"scan_ids":["scan-1"],"groups":[{"id":"group:unmentioned","level":"unknown"}],"next_offset":50}`
	out, err := RenderWorkerTaskAssignment(t.Context(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID: "parent", ProjectDir: t.TempDir(), AgentType: "web-researcher", WorkerJobID: "worker",
		Charter: testWorkerCharter("Challenge the single coordinator claim"), Scope: api.TaskScope{Mode: api.TaskScopeModeRead},
		ScanInventory: inventory,
	})
	testutil.FailErr(t, "render inventory assignment", err)
	if !strings.Contains(out, inventory) || !strings.Contains(out, "scan_query") {
		t.Fatalf("independent inventory or continuation tool missing: %s", out)
	}
}

func TestRenderWorkerTaskAssignment_rendersStructuredCharter(t *testing.T) {
	out, err := RenderWorkerTaskAssignment(context.Background(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:  "sess-inject-test",
		ProjectDir: t.TempDir(),
		Charter: api.WorkerTaskCharter{
			Goal:          "Expand engine.py and entities.py",
			SharedContext: "render(state: ViewState): void",
			KnownFacts:    []string{"The engine applies entity updates."},
			Constraints:   []string{"Keep public behavior unchanged."},
			DoneWhen:      []string{"Both modules have focused responsibilities."},
			ContextRefs:   []string{"engine.py", "entities.py"},
		},
		AgentType:    "implementer",
		WorkerJobID:  "job-abc-123",
		Scope:        api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"engine.py", "entities.py"}},
		MaxToolLoops: 20,
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	if strings.Contains(out, "User task (parent session):") {
		t.Fatalf("parent user task must not appear in worker charter:\n%s", out)
	}
	for _, want := range []string{"Goal:", "Expand engine.py", "Known facts:", "Constraints:", "Done when:", "Context references:", "render(state: ViewState): void", "private snapshot"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing charter section %q in %q", want, out)
		}
	}
}

func TestRenderWorkerTaskAssignmentForwardsAttachmentsWhenBriefSet(t *testing.T) {
	body := strings.Repeat("SECRET-BODY", 80)
	out, err := RenderWorkerTaskAssignment(context.Background(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:    "sess-inject-test",
		ProjectDir:   t.TempDir(),
		Charter:      testWorkerCharter("Analyze only the attached 33MB browser timeline JSON."),
		AgentType:    "repo-researcher",
		WorkerJobID:  "job-attach-1",
		Scope:        api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"."}},
		MaxToolLoops: 20,
		Attachments: []promptattach.ForwardedAttachment{{
			Filename:  "127.0.0.1-recording.json",
			MIME:      "application/json",
			Path:      "prompt-attachments/b3a260fb/127.0.0.1-recording.json",
			SizeBytes: 33309898,
			Kind:      promptattach.ForwardedPayload,
		}},
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	if strings.Contains(out, "User task (parent session):") {
		t.Fatalf("parent user task must not appear in worker charter:\n%s", out)
	}
	if strings.Contains(out, body) || strings.Contains(out, "SECRET-BODY") {
		t.Fatalf("assignment must not copy attachment body:\n%s", out)
	}
	if !strings.Contains(out, "Parent attachments") {
		t.Fatalf("missing attachment block:\n%s", out)
	}
	if !strings.Contains(out, "127.0.0.1-recording.json") {
		t.Fatalf("missing filename:\n%s", out)
	}
	if !strings.Contains(out, "prompt-attachments/b3a260fb/127.0.0.1-recording.json") {
		t.Fatalf("missing host-data path:\n%s", out)
	}
	if !strings.Contains(out, "33309898") {
		t.Fatalf("missing size:\n%s", out)
	}
	if !strings.Contains(out, `jq(path="prompt-attachments/b3a260fb/127.0.0.1-recording.json")`) {
		t.Fatalf("missing jq hint:\n%s", out)
	}
}

func TestWorkerAssignmentCarriesCoverageIndependentlyOfClaims(t *testing.T) {
	assignment := reviewcoverage.Assign(reviewcoverage.Facts{Gaps: []reviewcoverage.Fact{{ID: "gap/all", Kind: "partial", FileCount: 654, Paths: []string{"server/entry.go"}}}}, api.CoverageReview{Revision: "candidate"}, "check")
	in := WorkerTaskAssignmentInput{SessionID: "parent", ProjectDir: t.TempDir(), AgentType: "reviewer", WorkerJobID: "job", Charter: testWorkerCharter("Challenge one claim"), CoverageAssignment: &reviewcoverage.Binding{Subject: assignment, CoverageRequired: true}}
	out, err := RenderWorkerTaskAssignment(t.Context(), testInjectRenderer(t), in)
	testutil.FailErr(t, "render coverage assignment", err)
	page, _, err := reviewcoverage.Page(assignment, 0, 50)
	testutil.FailErr(t, "page coverage assignment", err)
	raw, err := json.Marshal(page)
	testutil.FailErr(t, "encode coverage assignment", err)
	if !strings.Contains(out, string(raw)) {
		t.Fatal("host scope lost from assignment")
	}
	in.CoverageAssignment = nil
	out, err = RenderWorkerTaskAssignment(t.Context(), testInjectRenderer(t), in)
	testutil.FailErr(t, "render ordinary assignment", err)
	if strings.Contains(out, assignment.Facts.Revision) {
		t.Fatal("ordinary worker inherited coverage contract")
	}
}

func TestWorkerAssignmentPagesLargeSubjects(t *testing.T) {
	binding := reviewcoverage.Binding{ID: "job", CoverageRequired: true, Subject: reviewcoverage.Assignment{Facts: reviewcoverage.Facts{Revision: "retained"}}}
	for i := 0; i < 51; i++ {
		binding.Subject.Facts.Obligations = append(binding.Subject.Facts.Obligations, reviewcoverage.Fact{ID: fmt.Sprintf("area/%d", i)})
	}
	data := BuildWorkerTaskAssignmentData(t.Context(), WorkerTaskAssignmentInput{CoverageAssignment: &binding})
	var page reviewcoverage.BindingPage
	testutil.FailErr(t, "decode paged prompt context", json.Unmarshal([]byte(data.CoverageAssignment), &page))
	if page.ID != "job" || page.NextCursor != "50" || len(page.Subject.Facts.Obligations) != 50 || page.Subject.Facts.Revision != "retained" {
		t.Fatalf("invalid paged context: %+v", page)
	}
	if len(binding.Subject.Facts.Obligations) != 51 {
		t.Fatal("projection mutated stored subject")
	}
}
