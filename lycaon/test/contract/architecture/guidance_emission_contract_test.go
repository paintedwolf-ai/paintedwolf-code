package contract

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

func TestGuidanceNoBannedInlineEmission(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	scan, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission sites in internal/", err)
	if len(scan.QueuePendingTextSites) > 0 {
		t.Fatalf("QueuePendingText called outside allowlist (use queueCoordinatorGuidanceNudge + hint templates):\n%s",
			strings.Join(scan.QueuePendingTextSites, "\n"))
	}
	if len(scan.GroundingNudgeMessageField) > 0 {
		t.Fatalf("ErrGroundingNudge must not set Message inline (use Code + Data + FormatCoordinatorNudge):\n%s",
			strings.Join(scan.GroundingNudgeMessageField, "\n"))
	}
	if len(scan.InlineCodeNudgeLiterals) > 0 {
		t.Fatalf("ad-hoc Code: … — string literals in coordinator packages (register hint + pongo template):\n%s",
			strings.Join(scan.InlineCodeNudgeLiterals, "\n"))
	}
}

func TestGuidanceFormatCallsRegisteredInHintCodes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	registered := make(map[string]bool, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = true
	}
	scan, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission sites in internal/", err)
	var unknown []string
	for code := range scan.FormatCalls {
		if !registered[code] {
			unknown = append(unknown, code)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Fatalf(".Format / FormatCoordinatorNudge called with codes not in hint registry: %v", unknown)
	}
}

func TestRecoverableHintCodesRenderFullRejectBlock(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	f := guidance.NewStaticRejectFormatter(cfg)

	for code, entry := range cfg.HintCodes {
		// OAR hints are never evaluated and carry no scenarios.
		if guidance.CopyOnlyEmit(entry.Emit) || entry.Kind == "hint" {
			continue
		}
		if entry.Severity == "info" && !strings.HasPrefix(entry.Emit, "guard:") && !strings.HasPrefix(entry.Emit, "rule:") {
			continue
		}
		if strings.TrimSpace(entry.What) == "" && strings.TrimSpace(entry.Message) == "" {
			t.Fatalf("hint %q missing what or message", code)
		}
		// Scenarios supply the template inputs.
		if len(entry.Scenarios) == 0 {
			t.Fatalf("hint %q missing scenarios", code)
		}
		assertBlock := func(label string, vars map[string]any) {
			raw, err := f.Format(code, vars)
			if err != nil {
				t.Fatalf("hint %q (%s) Format: %v", code, label, err)
			}
			if !strings.Contains(raw, "Code: "+code) {
				t.Fatalf("hint %q (%s) reject block lacks its code:\n%s", code, label, raw)
			}
		}
		assertBlock("default", map[string]any{
			"tool":    renderToolFor(entry, "read"),
			"path":    "src/main.go",
			"profile": "coordinator", "leg_id": "leg-1", "count": 3,
		})
		for _, sc := range entry.Scenarios {
			assertBlock(sc.ID, guidance.ScenarioVars(entry, sc))
		}
	}
}

// Every code an emission site names is registered. Whether every registered
// code is emitted is judged by arch-lint against what the product reaches.
func TestEmissionSitesNameRegisteredCodes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	registered := make(map[string]bool, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = true
	}
	scan, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission sites in internal/", err)

	var emittedNotRegistered []string
	for code := range scan.FormatCalls {
		if !registered[code] {
			emittedNotRegistered = append(emittedNotRegistered, code)
		}
	}
	for code := range scan.HintCodeRefs {
		if !registered[code] {
			emittedNotRegistered = append(emittedNotRegistered, code)
		}
	}
	sort.Strings(emittedNotRegistered)
	if len(emittedNotRegistered) > 0 {
		t.Fatalf("emit sites reference codes missing from hint registry: %v", emittedNotRegistered)
	}
}
