package contract

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestStockPolicyCopyUsesFrozenBindings fails when a pack copy member (or
// x-paintedwolf-message) carries a construct outside Appendix C or names a fact
// this engine does not declare.
func TestStockPolicyCopyUsesFrozenBindings(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") || !strings.Contains(path, "/policy/") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var doc map[string]any
		if uErr := yaml.Unmarshal(raw, &doc); uErr != nil {
			t.Errorf("%s: %v", path, uErr)
			return nil
		}
		if _, isRule := doc["oar"]; !isRule {
			return nil // Host presentation entries use the separate guidance renderer.
		}
		rel, _ := filepath.Rel(root, path)
		for _, field := range copyTexts(doc) {
			if !strings.Contains(field, "{{") && !strings.Contains(field, "{%") {
				continue
			}
			bindings, pErr := oarcore.ParseCopy(field)
			if pErr != nil {
				t.Errorf("%s: illegal copy construct %s", rel, pErr.Error())
				continue
			}
			for _, b := range bindings {
				if !oar.DeclaresCopyFact(b.Name) {
					t.Errorf("%s: undeclared copy fact %s", rel, b.Name)
				}
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk policy", err)
}

func copyTexts(doc map[string]any) []string {
	var out []string
	add := func(v any) {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if copy, ok := doc["copy"].(map[string]any); ok {
		for _, v := range copy {
			add(v)
		}
	}
	for _, key := range []string{"what", "cause", "why", "fix", "instead", "message", "x-paintedwolf-message"} {
		add(doc[key])
	}
	return out
}

// groundingRepairRecallCodes are the grounding and citation repairs that
// recover a recorded observation. Their copy must stand alone: the
// native-recall-tool instruction unit may be omitted from the turn.
var groundingRepairRecallCodes = []string{
	"AMBIENT_GROUNDING_ESCALATED",
	"AMBIENT_UNGROUNDED_COMPLETION",
	"FINDING_UNGROUNDED",
	"INVEST_CITATION_UNVERIFIABLE",
	"SUBMIT_VERDICT_CITATION_UNGROUNDED",
	"SUBMIT_VERDICT_CITATIONS_REQUIRED",
	"SURFACE_NOTE_HANDLE_UNGROUND",
	"SURFACE_NOTE_UNGROUNDED",
	"SYNTH_CITATION_UNVERIFIABLE",
}

func TestGroundingRepairCopyNamesRecallOnlyWhenCallable(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load stock policy", err)
	for _, code := range groundingRepairRecallCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("%s missing from stock policy", code)
		}
		if entry.Requires == nil || !slices.Contains(entry.Requires.Facts, "paintedwolf.can_recall") {
			t.Errorf("%s does not require paintedwolf.can_recall", code)
		}
		for _, callable := range []bool{true, false} {
			facts := map[string]any{"paintedwolf.can_recall": callable, "paintedwolf.can_read": true}
			var body strings.Builder
			for _, field := range []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead} {
				body.WriteString(oarcore.RenderCopy(field, facts) + "\n")
			}
			if callable && !strings.Contains(body.String(), `recall(query="`) {
				t.Errorf("%s omits recall while it is callable:\n%s", code, body.String())
			}
			if !callable && strings.Contains(body.String(), "recall") {
				t.Errorf("%s names recall while it is not callable:\n%s", code, body.String())
			}
		}
	}
}

// Repairs can only point at recall where every turn offers it.
func TestRecallStickyOnEveryProfileAndSurface(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	if len(profiles) == 0 {
		t.Fatal("no tool profiles loaded")
	}
	for _, prof := range profiles {
		if !prof.ToolAllowed("recall") || !prof.ToolSticky("recall") {
			t.Errorf("profile %s does not keep recall sticky", prof.ID)
		}
	}
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surface catalog", err)
	for _, surfaceID := range catalog.SurfaceIDs() {
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		contractcheck.FailErr(t, "CompileToolPlan "+surfaceID, err)
		if !slices.Contains(plan.ImmediateNames(), "recall") {
			t.Errorf("surface %s does not offer recall immediately", surfaceID)
		}
	}
}

func TestCitationGroundingKickNamesRecallOnlyWhenCallable(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, callable := range []bool{true, false} {
		out, err := engine.RenderKick(context.Background(), "coordinator-citation-grounding", map[string]any{
			"attempt": 1, "max_attempts": 3, "can_recall": callable,
		})
		contractcheck.FailErr(t, "render coordinator-citation-grounding", err)
		if got := strings.Contains(out, `recall(query="handle:`); got != callable {
			t.Errorf("can_recall=%v: names recall = %v:\n%s", callable, got, out)
		}
	}
}
