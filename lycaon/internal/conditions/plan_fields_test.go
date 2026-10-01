package conditions_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func stubWithDepth(depth string) string {
	head := "---\ntitle: Ship it\n"
	if depth != "" {
		head += "research_depth: " + depth + "\n"
	}
	return head + "---\n" +
		"## Goal\n\nx\n\n## Assumptions\n\nx\n\n" +
		"## Plan implementation scope\n\n**Size:** small\n\n" +
		"## Plan breaking changes\n\nnone\n\n## Approach\n\nship it\n"
}

func TestResearchRequiredReadsTheDeclaredValueOnly(t *testing.T) {
	cases := []struct {
		name  string
		plan  string
		valid bool
		want  bool
	}{
		{name: "none closes research", plan: stubWithDepth("none"), valid: true},
		{name: "light opens research", plan: stubWithDepth("light"), valid: true, want: true},
		{name: "thorough opens research", plan: stubWithDepth("thorough"), valid: true, want: true},
		// Normalized equality: trimmed and lowercased, nothing else.
		{name: "case and space are normalized", plan: stubWithDepth("  None  "), valid: true},
		{name: "a sentence is not an answer", plan: stubWithDepth("no research needed here")},
		{name: "a word outside the vocabulary is not an answer", plan: stubWithDepth("skip")},
		{name: "an absent field is unanswered", plan: stubWithDepth("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conditions.PlanStubValidText(tc.plan); got != tc.valid {
				t.Fatalf("PlanStubValidText = %v, want %v (missing: %v)",
					got, tc.valid, conditions.PlanStubMissingRequired(tc.plan))
			}
			if got := conditions.ResearchRequiredText(tc.plan); got != tc.want {
				t.Fatalf("ResearchRequiredText = %v, want %v", got, tc.want)
			}
		})
	}
}

// The blueprint body cannot move the gate. Whatever a plan says in prose, the
// declared value is the answer.
func TestPlanBodyProseCannotChangeTheResearchGate(t *testing.T) {
	plan := stubWithDepth("light") +
		"\n## Notes\n\nNo research is required, skip it, none needed, n/a.\n"
	if !conditions.ResearchRequiredText(plan) {
		t.Fatal("prose in the body closed the research gate")
	}
}

func TestPlanBlueprintFieldsCatalogue(t *testing.T) {
	fields, err := conditions.PlanBlueprintFields()
	testutil.FailErr(t, "PlanBlueprintFields", err)
	if len(fields) == 0 {
		t.Fatal("no plan blueprint fields declared")
	}
	var field conditions.PlanField
	for _, f := range fields {
		if f.Key == conditions.ResearchDepthFieldKey {
			field = f
		}
	}
	if field.Key == "" {
		t.Fatalf("%s is not declared", conditions.ResearchDepthFieldKey)
	}
	opens := 0
	for _, v := range field.Values {
		if v.OpensResearch {
			opens++
		}
	}
	if opens == 0 || opens == len(field.Values) {
		t.Fatalf("research_depth needs at least one value on each side of the gate; %d of %d open it",
			opens, len(field.Values))
	}
	// The agent-facing prompt names the key and every value it may write.
	prompt := field.Prompt()
	for _, id := range field.ValueIDs() {
		if !strings.Contains(prompt, id) {
			t.Errorf("prompt %q omits value %q", prompt, id)
		}
	}
	if !strings.Contains(prompt, conditions.ResearchDepthFieldKey) {
		t.Errorf("prompt %q omits the key", prompt)
	}
}

func TestPlanStubRequiredLabelsCoverHeadingsAndFields(t *testing.T) {
	labels := strings.Join(conditions.PlanStubRequiredLabels(), " | ")
	for _, heading := range []string{"## Goal", "## Assumptions", "## Approach"} {
		if !strings.Contains(labels, heading) {
			t.Errorf("required labels omit heading %q", heading)
		}
	}
	// The depth is a frontmatter field, so it must not also be demanded as a heading.
	if strings.Contains(labels, "## Plan research depth") {
		t.Error("research depth demanded as a heading as well as a field")
	}
	if !strings.Contains(labels, conditions.ResearchDepthFieldKey) {
		t.Errorf("required labels omit the research_depth field: %s", labels)
	}
	if !strings.Contains(labels, conditions.PlanStubTitleLabel) {
		t.Errorf("required labels omit title: %s", labels)
	}
}
