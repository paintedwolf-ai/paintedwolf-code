package prompts

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorRender_BundledPartial(t *testing.T) {
	ResetPersonaContractCache()
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		TemplateRef: "partials/finish-handoff.md",
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Fatal("bundled partial rendered empty")
	}
}

func TestAuthorRender_PersonaHasRequiredHeadings(t *testing.T) {
	ResetPersonaContractCache()
	root := configlayout.FindModuleRoot()
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot: root,
		AgentID:    "implementer",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	contract, err := LoadPersonaContract()
	if err != nil {
		t.Fatalf("LoadPersonaContract: %v", err)
	}
	for _, heading := range contract.RequiredHeadingsRendered {
		if !strings.Contains(res.Output, heading) {
			t.Fatalf("stock persona is missing %q", heading)
		}
	}
	if len(res.Violations) != 0 {
		t.Fatalf("stock persona reported violations: %v", res.Violations)
	}
}

func TestAuthorRender_ProjectOverlayWins(t *testing.T) {
	ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "## Finish\n\nProject override body.\n"
	if err := os.WriteFile(filepath.Join(dir, "finish-handoff.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		ProjectDir:  project,
		TemplateRef: "partials/finish-handoff.md",
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "Project override body.") {
		t.Fatalf("overlay did not win:\n%s", res.Output)
	}
}

func TestPromptSnapshotCapturesOverrideBytesAndRevision(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	path := filepath.Join(dir, "finish-handoff.md")
	if err := os.WriteFile(path, []byte("first snapshot\n"), 0o600); err != nil {
		t.Fatalf("write first override: %v", err)
	}
	base := NewFileTemplateEngineLayers(PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}).WithProjectOverlay(project)
	first, err := base.Snapshot()
	if err != nil {
		t.Fatalf("capture first snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("second snapshot\n"), 0o600); err != nil {
		t.Fatalf("write second override: %v", err)
	}
	second, err := base.Snapshot()
	if err != nil {
		t.Fatalf("capture second snapshot: %v", err)
	}
	if first.Revision() == second.Revision() {
		t.Fatal("override change must produce a new prompt revision")
	}
	firstOut, err := first.Render(context.Background(), "partials/finish-handoff.md", nil)
	if err != nil {
		t.Fatalf("render first snapshot: %v", err)
	}
	secondOut, err := second.Render(context.Background(), "partials/finish-handoff.md", nil)
	if err != nil {
		t.Fatalf("render second snapshot: %v", err)
	}
	if !strings.Contains(firstOut, "first snapshot") || !strings.Contains(secondOut, "second snapshot") {
		t.Fatalf("snapshots did not preserve their own bytes: first=%q second=%q", firstOut, secondOut)
	}
}

func TestAuthorRender_OverlayParseErrorFailsClosed(t *testing.T) {
	ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-handoff.md"), []byte("{% if unclosed %}\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	_, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		ProjectDir:  project,
		TemplateRef: "partials/finish-handoff.md",
	})
	if err == nil {
		t.Fatal("expected a parse error, got a render")
	}
	if !strings.Contains(err.Error(), "finish-handoff.md") {
		t.Fatalf("error does not name the template: %v", err)
	}
}

func TestAuthorRender_CheckCatchesStrippedHeadings(t *testing.T) {
	ResetPersonaContractCache()
	defer ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "agents")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "implementer.md"), []byte("## Grounding\n\nStripped.\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		ProjectDir: project,
		AgentID:    "implementer",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if len(res.Violations) == 0 {
		t.Fatal("stripped override reported no violations")
	}
	var sawHeading bool
	for _, v := range res.Violations {
		if strings.HasPrefix(v, "missing_heading:") {
			sawHeading = true
		}
	}
	if !sawHeading {
		t.Fatalf("violations do not name a missing heading: %v", res.Violations)
	}
}

func TestAuthorRender_VarsReachTemplate(t *testing.T) {
	ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-handoff.md"), []byte("focus={{ focus }}\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		ProjectDir:  project,
		TemplateRef: "partials/finish-handoff.md",
		Vars:        map[string]any{"focus": "triage"},
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "focus=triage") {
		t.Fatalf("--var did not reach the template:\n%s", res.Output)
	}
}

func TestAuthorRender_RefAndAgentAreExclusive(t *testing.T) {
	_, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		TemplateRef: "partials/finish-handoff.md",
		AgentID:     "implementer",
	})
	if err == nil {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestAuthorRender_SkillBodyStock(t *testing.T) {
	res, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		SkillName:  "write-a-skill",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "# Write a skill") {
		t.Fatalf("skill body did not render:\n%s", res.Output)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("stock skill reported violations: %v", res.Violations)
	}
}

func TestAuthorRender_SkillResource(t *testing.T) {
	res, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		SkillName:  "write-a-skill",
		Resource:   "references/FORMAT.md",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "SKILL.md field table") {
		t.Fatalf("skill resource did not render:\n%s", res.Output)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("stock skill resource reported violations: %v", res.Violations)
	}
}

func TestAuthorRender_SkillVarOverlay(t *testing.T) {
	res, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		SkillName:  "orchestrate-a-large-task",
		Vars:       map[string]any{"max_in_flight": 2},
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "at most **2** legs") {
		t.Fatalf("--var did not overlay the sizing var:\n%s", res.Output)
	}
}

func TestAuthorRender_SkillRefAndSkillAreExclusive(t *testing.T) {
	_, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		TemplateRef: "partials/finish-handoff.md",
		SkillName:   "write-a-skill",
	})
	if err == nil {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestAuthorRender_SkillNotFound(t *testing.T) {
	_, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		SkillName:  "no-such-skill-anywhere",
	})
	if err == nil || !strings.Contains(err.Error(), "not found in the effective catalog") {
		t.Fatalf("expected catalog miss, got %v", err)
	}
}

func TestAuthorRender_SkillProjectLiteral(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "skills", "notes-dump")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: notes-dump\ndescription: A project skill that must render literally.\n---\n\nKeep {{ not_a_var }} as written.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	res, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		ProjectDir: project,
		SkillName:  "notes-dump",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "Keep {{ not_a_var }} as written.") {
		t.Fatalf("project skill was templated:\n%s", res.Output)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("literal project skill reported violations: %v", res.Violations)
	}
}

func TestAuthorRender_SkillBudgetViolation(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "skills", "big-notes")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: big-notes\ndescription: A project skill over the body budget.\n---\n\n" + strings.Repeat("Filler prose for the budget check.\n", 200)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	res, err := AuthorRender(t.Context(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		ProjectDir: project,
		SkillName:  "big-notes",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if len(res.Violations) != 1 || !strings.HasPrefix(res.Violations[0], "budget_exceeded:big-notes:") {
		t.Fatalf("violations = %v, want one budget_exceeded:big-notes", res.Violations)
	}
	if !strings.HasSuffix(res.Violations[0], fmt.Sprintf("/%d", skills.BundledBodyBudget)) {
		t.Fatalf("violation %q lacks the budget cap", res.Violations[0])
	}
}

func TestLeftoverTemplateMarkers(t *testing.T) {
	source := "Literal: {% verbatim %}{{ keep }}{% endverbatim %} and {{ sized }}.\n"
	if leftoverTemplateMarkers(source, "Literal: {{ keep }} and 4096.\n") {
		t.Fatal("verbatim-protected markers must not be flagged")
	}
	if !leftoverTemplateMarkers(source, "Literal: {{ keep }} and {{ sized }}.\n") {
		t.Fatal("an unrendered marker outside verbatim must be flagged")
	}
}

func TestCheckSkillRender_UnreachableTool(t *testing.T) {
	profileID, surfaceID, err := skillPreviewProfile("implementer", "")
	if err != nil {
		t.Fatalf("skillPreviewProfile: %v", err)
	}
	calls := skills.Skill{Name: "calls-task", Body: "Start a leg with `task` and read `skills_read`.\n"}
	violations, err := checkSkillRender(calls, "", calls.Body, calls.Body, true, profileID, surfaceID)
	if err != nil {
		t.Fatalf("checkSkillRender: %v", err)
	}
	if len(violations) != 1 || violations[0] != "unreachable_tool:task" {
		t.Fatalf("violations = %v, want [unreachable_tool:task]", violations)
	}
	held := skills.Skill{Name: "calls-command", Body: "Inspect with `command`.\n"}
	violations, err = checkSkillRender(held, "", held.Body, held.Body, true, profileID, surfaceID)
	if err != nil {
		t.Fatalf("checkSkillRender: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("a held tool must not be flagged: %v", violations)
	}
	user := skills.Skill{Name: "user-calls", Body: "Start a leg with `task`.\n", UserProvided: true}
	violations, err = checkSkillRender(user, "", user.Body, user.Body, true, profileID, surfaceID)
	if err != nil {
		t.Fatalf("checkSkillRender: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("user-provided skills must not get unreachable checks: %v", violations)
	}
}
