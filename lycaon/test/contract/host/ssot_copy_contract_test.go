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
