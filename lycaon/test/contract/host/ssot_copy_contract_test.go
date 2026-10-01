package contract

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var laneSSurveyCopyRelPaths = []string{
	"lycaon/config/packs/painted-wolf/platform/shared/partials/pack-board-legend.md",
	"lycaon/config/packs/painted-wolf/platform/policy",
}

var surveyArchetypeRelPaths = []string{
	"lycaon/config/packs/painted-wolf/platform/shared/archetypes/explore_readonly.md",
}

func TestCompactionWorkerSummaryBudgetMatchesLimitsDefault(t *testing.T) {
	t.Parallel()
	cfg := compaction.DefaultCompactionConfig()
	windows, err := modelinfo.LoadModelContextWindows()
	contractcheck.FailErr(t, "LoadModelContextWindows", err)
	live, _, err := llm.ApplyLiveBudget(cfg, llm.ModelPolicy{}, nil, windows)
	contractcheck.FailErr(t, "ApplyLiveBudget", err)
	if live.MaxWorkerSummaryChars != limits.DefaultWorkerSummaryMaxChars {
		t.Fatalf("live MaxWorkerSummaryChars = %d want limits.DefaultWorkerSummaryMaxChars %d",
			live.MaxWorkerSummaryChars, limits.DefaultWorkerSummaryMaxChars)
	}
}

func TestCoordinatorPolicyWorkerSummaryMatchesCompactionYAML(t *testing.T) {
	t.Parallel()
	budget := limits.DefaultWorkerSummaryMaxChars
	vars := prompts.CoordinatorPolicyTemplateVars([]string{"find", "grep", "read", "task"}, nil)
	got, ok := vars["worker_summary_max_chars"].(int)
	if !ok || got != budget {
		t.Fatalf("worker_summary_max_chars = %v want %d from limits default (YAML omits key)", vars["worker_summary_max_chars"], budget)
	}
}

func TestCoordinatorPolicyProgressLimitsMatchProgressPackage(t *testing.T) {
	t.Parallel()
	vars := prompts.CoordinatorPolicyTemplateVars([]string{"find", "grep", "read", "task", "update_progress"}, nil)
	if got, ok := vars["max_author_progress_lines"].(int); !ok || got != progress.MaxAuthorProgressLines {
		t.Fatalf("max_author_progress_lines = %v want %d", vars["max_author_progress_lines"], progress.MaxAuthorProgressLines)
	}
	if got, ok := vars["max_progress_label_chars"].(int); !ok || got != progress.MaxLabelRunes {
		t.Fatalf("max_progress_label_chars = %v want %d", vars["max_progress_label_chars"], progress.MaxLabelRunes)
	}
}

func TestUpdateProgressToolSchemaMatchesProgressLimits(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load tools/schemas", err)
	entry, ok := cfg.Tools["update_progress"]
	if !ok {
		t.Fatal("tools/schemas missing update_progress")
	}
	props, _ := entry.Schema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("update_progress schema missing properties")
	}
	content, _ := props["content"].(map[string]any)
	if content == nil {
		t.Fatal("update_progress schema missing content property")
	}
	contentDesc, _ := content["description"].(string)
	wantLines := strconv.Itoa(progress.MaxAuthorProgressLines)
	wantLabel := strconv.Itoa(progress.MaxLabelRunes)
	// The content argument supplies both caps.
	if !strings.Contains(contentDesc, "max "+wantLines+" lines") && !strings.Contains(contentDesc, "Max "+wantLines+" lines") {
		t.Fatalf("update_progress content arg missing max %s lines: %q", wantLines, contentDesc)
	}
	if !strings.Contains(contentDesc, "≤"+wantLabel+"c") {
		t.Fatalf("update_progress content arg missing ≤%sc label cap: %q", wantLabel, contentDesc)
	}
}

func TestWorkerKickMarkerFallbackIsMarkerOnly(t *testing.T) {
	t.Parallel()
	marker := regexp.MustCompile(`^\[host:worker-[a-z-]+\]$`)
	ctx := context.Background()
	for _, kickID := range []string{anchor.InformRender(anchor.WorkerCloseout), anchor.InformRender(anchor.WorkerCancelCloseout), anchor.InformRender(anchor.WorkerSummaryTrim), anchor.InformRender(anchor.WorkerCitationGrounding)} {
		got := workercloseout.RenderWorkerKick(ctx, nil, kickID, map[string]any{
			"max_chars":    12000,
			"actual_chars": 21000,
		})
		if !marker.MatchString(got) {
			t.Fatalf("kick %q marker fallback = %q want [host:worker-*] only", kickID, got)
		}
	}
}

func TestWorkerKickGoSourceHasNoTemplateProseFallback(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "session", "workercloseout", "worker_kick.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read worker_kick.go", err)
	violations := contractcheck.CheckPatterns(path, string(data), contractcheck.ForbiddenWorkerKickFallbackProsePatterns, contractcheck.SkipCommentLine)
	contractcheck.FailViolations(t, "worker_kick.go duplicates kick template prose in Go fallback", violations)
}

func TestWorkerKickTemplatesIncludeProseOnlyPartial(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	kicksDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "guidance")
	for _, name := range []string{"worker-closeout.md", "worker-cancel-closeout.md", "worker-summary-trim.md"} {
		data, err := os.ReadFile(filepath.Join(kicksDir, name))
		contractcheck.FailErr(t, "read "+name, err)
		if !strings.Contains(string(data), `partials/completion-envelope-only-turn.md`) {
			t.Fatalf("%s must include completion-envelope-only-turn partial", name)
		}
	}
	// Coordinator closeouts use prose with a citations fence.
	coordDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "guidance")
	coordCloseout, err := os.ReadFile(filepath.Join(coordDir, "coordinator-closeout.md"))
	contractcheck.FailErr(t, "read coordinator-closeout.md", err)
	if strings.Contains(string(coordCloseout), "completion-envelope-only-turn.md") {
		t.Fatal("coordinator-closeout.md must not include the worker-only envelope partial")
	}
	if !strings.Contains(string(coordCloseout), "cited_evidence") {
		t.Fatal("coordinator-closeout.md must teach the citations fence")
	}
}

func TestProjectPathPresentationIsSharedAcrossCoordinatorPrompts(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	partialRef := `partials/project-path-presentation.md`
	for _, rel := range []string{
		"lycaon/config/packs/painted-wolf/platform/agents/prompts/coordinator-core.md",
		"lycaon/config/packs/painted-wolf/platform/guidance/coordinator-closeout.md",
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		if !strings.Contains(string(data), partialRef) {
			t.Fatalf("%s must include %s", rel, partialRef)
		}
	}
	partialPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "project-path-presentation.md")
	partial, err := os.ReadFile(partialPath)
	contractcheck.FailErr(t, "read project path presentation partial", err)
	for _, want := range []string{"[label](full/repo-relative/path/)", "never bare words", "[label](source://root-id/path/to/file)"} {
		if !strings.Contains(string(partial), want) {
			t.Fatalf("project path presentation partial missing %q", want)
		}
	}
}

func TestLaneSSurveyCopyNoStaleOrdering(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var violations []string
	for _, rel := range laneSSurveyCopyRelPaths {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		contractcheck.FailErr(t, "stat "+rel, err)
		if info.IsDir() {
			entries, err := os.ReadDir(path)
			contractcheck.FailErr(t, "read dir "+rel, err)
			for _, ent := range entries {
				if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
					continue
				}
				sub := filepath.Join(path, ent.Name())
				data, err := os.ReadFile(sub)
				contractcheck.FailErr(t, "read "+sub, err)
				violations = append(violations, contractcheck.CheckPatterns(sub, string(data), contractcheck.LaneSSurveyStaleCopyPatterns, nil)...)
			}
			continue
		}
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+rel, err)
		violations = append(violations, contractcheck.CheckPatterns(path, string(data), contractcheck.LaneSSurveyStaleCopyPatterns, nil)...)
	}
	contractcheck.FailViolations(t, "Lane S survey copy uses stale read/grep ordering (want find-first)", violations)
}

func TestLaneSSurveyCoordinatorHintsMentionFindFirst(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, code := range []string{"COORDINATOR_READ_OUTSIDE_SCOPE"} {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("missing hint %q", code)
		}
		prose := strings.Join([]string{entry.Message, entry.Fix, entry.Instead}, "\n")
		if !strings.Contains(prose, "find") {
			t.Fatalf("hint %q prose must mention find for find-first Lane S copy:\n%s", code, prose)
		}
		if stale := firstStaleLaneSPattern(prose); stale != "" {
			t.Fatalf("hint %q contains stale Lane S ordering %q", code, stale)
		}
	}
}

func TestFinishHandoffPartialRendersCompactionBudget(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	budget := loadCompactionWorkerSummaryBudget(t, root)
	vars := map[string]any{}
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: lycaonRoot})
	out, err := engine.Render(context.Background(), "partials/finish-handoff.md", vars)
	contractcheck.FailErr(t, "render finish-handoff", err)
	if !strings.Contains(out, strconv.Itoa(budget)) {
		t.Fatalf("finish-handoff missing budget %d:\n%s", budget, out)
	}
	if !strings.Contains(out, "WORKER_SUMMARY_TOO_LONG") {
		t.Fatal("finish-handoff must document WORKER_SUMMARY_TOO_LONG reject path")
	}
	if !strings.Contains(out, "Remaining tool runway is contingency, not scope") {
		t.Fatal("finish-handoff must stop workers from treating unused runway as scope")
	}
}

func TestSurveyArchetypesIncludeFirstPassLadder(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range surveyArchetypeRelPaths {
		data, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		text := string(data)
		// The survey ladder is an orientation unit; archetypes render the slot.
		if !strings.Contains(text, `{{ units.orientation }}`) {
			t.Fatalf("%s must render the orientation units", rel)
		}
		if strings.Contains(text, `partials/survey-find-first`) {
			t.Fatalf("%s must not include find-first teaching", rel)
		}
	}
}

func TestSecurityScanSurveyArchetypeComposition(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	rel := "lycaon/config/packs/painted-wolf/scan-guidance/shared/archetypes/security_scan_survey.md"
	data, err := os.ReadFile(filepath.Join(root, rel))
	contractcheck.FailErr(t, "read "+rel, err)
	text := string(data)
	for _, forbidden := range []string{
		`partials/survey-find-first-gated.md`,
		`partials/survey-find-first.md`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s must not include %q", rel, forbidden)
		}
	}
	const targetedContext = "partials/security-scan-targeted-context.md"
	if !strings.Contains(text, targetedContext) {
		t.Fatalf("%s no longer includes %s", rel, targetedContext)
	}
}

func TestExploreReadonlyBoundsNegativeSearches(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	rel := "lycaon/config/packs/painted-wolf/platform/shared/archetypes/explore_readonly.md"
	data, err := os.ReadFile(filepath.Join(root, rel))
	contractcheck.FailErr(t, "read "+rel, err)
	text := string(data)
	for _, required := range []string{
		"Combine related absence checks",
		"Repository-wide absence proof",
		"remaining_risk",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("%s missing bounded negative-search rule %q", rel, required)
		}
	}
}

func TestSurveyAgentPromptsDoNotDuplicatePlaybookChain(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, tc := range []struct {
		pack, name string
	}{
		{"recon-pack", "path-explorer.md"},
		{"recon-pack", "repo-researcher.md"},
	} {
		path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", tc.pack, "agents", "prompts", tc.name)
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+tc.name, err)
		text := string(data)
		for _, forbidden := range []string{
			"repo_map → find",
			"find → grep → read",
			"(repo_map",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s duplicates survey playbook chain (%q); trust archetype partials", tc.name, forbidden)
			}
		}
	}
}

func TestOARSpecVersionFrozenAt10(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemaPath := filepath.Join(root, "schemas", "oar", "oar.schema.json")
	raw, err := os.ReadFile(schemaPath)
	contractcheck.FailErr(t, "read vendored oar.schema.json", err)
	if !strings.Contains(string(raw), `"oar"`) {
		t.Fatal("vendored oar.schema.json missing the oar format marker")
	}
	if strings.Contains(string(raw), `"spec_version"`) {
		t.Fatal("vendored oar.schema.json contains spec_version — refresh from the standard")
	}
	sample, err := os.ReadFile(filepath.Join(root, "schemas", "fixtures", "oar-sample-rule.json"))
	contractcheck.FailErr(t, "read oar-sample-rule.json", err)
	if !strings.Contains(string(sample), `"oar": "1.0"`) && !strings.Contains(string(sample), `"oar":"1.0"`) {
		t.Fatal("schemas/fixtures/oar-sample-rule.json must declare oar 1.0")
	}
}

func loadCompactionWorkerSummaryBudget(t *testing.T, root string) int {
	t.Helper()
	cfg := compaction.DefaultCompactionConfig()
	windows, err := modelinfo.LoadModelContextWindows()
	contractcheck.FailErr(t, "LoadModelContextWindows", err)
	live, _, err := llm.ApplyLiveBudget(cfg, llm.ModelPolicy{}, nil, windows)
	contractcheck.FailErr(t, "ApplyLiveBudget", err)
	if live.MaxWorkerSummaryChars <= 0 {
		t.Fatal("compaction MaxWorkerSummaryChars must be positive after ApplyLiveBudget")
	}
	return live.MaxWorkerSummaryChars
}

func firstStaleLaneSPattern(text string) string {
	for _, re := range contractcheck.LaneSSurveyStaleCopyPatterns {
		if re.MatchString(text) {
			return re.String()
		}
	}
	return ""
}
