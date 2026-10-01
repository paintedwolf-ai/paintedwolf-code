package contract

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tsparse"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
	"gopkg.in/yaml.v3"
)

func TestSpillFailureGuidancePreservesCompletedEffects(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load spill feedback catalogue", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	out, err := guidance.NewStaticRejectFormatter(cfg).Format("TOOL_OUTPUT_SPILL_UNAVAILABLE", map[string]any{
		"tool": "git_commit", "execution_outcome": "completed", "bytes": 20000,
	})
	contractcheck.FailErr(t, "render spill failure", err)
	for _, want := range []string{"Code: TOOL_OUTPUT_SPILL_UNAVAILABLE", "completed", "20000", "Do not repeat a mutation"} {
		if !strings.Contains(out, want) {
			t.Fatalf("spill failure lost recovery contract %q: %s", want, out)
		}
	}
}

func TestSourceParsingRejectionsRetainMeasuredCause(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load parsing feedback catalogue", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	for _, tc := range []struct{ code, tool, reason string }{
		{"MUTATION_PARSE_INCOMPLETE", "write", "timeout"},
		{"MUTATION_PARSE_INCOMPLETE", "edit", "canceled"},
		{"MUTATION_PARSE_FAILED", "write", "panic"},
		{"SOURCE_PARSE_INCOMPLETE", "grep", "iteration_limit"},
		{"SOURCE_PARSE_FAILED", "read", "parser_error"},
	} {
		t.Run(tc.code+"/"+tc.reason, func(t *testing.T) {
			failure := &tsparse.Failure{Reason: tc.reason, Language: "swift", TimeoutMS: 30000, ElapsedMS: 30017, SourceBytes: 6236, ParsedBytes: 431}
			data := failure.Facts("candidate")
			data["tool"], data["path"] = tc.tool, "Sources/Level.swift"
			out, err := formatter.Format(tc.code, data)
			contractcheck.FailErr(t, "render parser rejection", err)
			for _, want := range []string{"Code: " + tc.code, "Sources/Level.swift", tc.reason, "swift", "30000", "30017", "6236", "431", "candidate"} {
				if !strings.Contains(out, want) {
					t.Fatalf("parser rejection lost %q: %s", want, out)
				}
			}
			if strings.Contains(out, "syntax_override_reason") {
				t.Fatalf("rejection teaches parser override: %s", out)
			}
		})
	}
}

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
		if guidance.CopyOnlyEmit(entry.Emit) {
			continue
		}
		if entry.Severity == "info" && !strings.HasPrefix(entry.Emit, "guard:") && !strings.HasPrefix(entry.Emit, "rule:") {
			continue
		}
		raw, err := f.Format(code, map[string]any{
			"tool":    renderToolFor(entry, "read"),
			"path":    "src/main.go",
			"profile": "coordinator", "leg_id": "leg-1", "count": 3,
		})
		if err != nil {
			t.Fatalf("hint %q Format: %v", code, err)
		}
		// Advisory effects never claim the attempted action was rejected.
		lead := "Rejected:"
		if strings.TrimSpace(entry.Effect) == "warn" || strings.TrimSpace(entry.Effect) == "nudge" {
			lead = ">>>"
		}
		for _, want := range []string{lead, "Fix:", "Code: " + code} {
			if !strings.Contains(raw, want) {
				t.Fatalf("hint %q reject block missing %q:\n%s", code, want, raw)
			}
		}
		if strings.TrimSpace(entry.Instead) == "" {
			t.Fatalf("hint %q missing instead (branch instruction)", code)
		}
		if strings.TrimSpace(entry.What) == "" && strings.TrimSpace(entry.Message) == "" {
			t.Fatalf("hint %q missing what or message", code)
		}
		if strings.TrimSpace(entry.Fix) == "" {
			t.Fatalf("hint %q missing fix", code)
		}
		// Scenarios supply template inputs and expected rendered output.
		if len(entry.Scenarios) == 0 {
			t.Fatalf("hint %q missing scenarios", code)
		}
	}
}

func TestRecoverableHintCodesHaveEmissionSite(t *testing.T) {
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
	ruleCodes, err := guidancescan.CodesReferencedInRulesYAML(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "host", "posture-rules"))
	contractcheck.FailErr(t, "scan hint codes referenced in rules YAML", err)
	profileCodes, err := guidancescan.CodesReferencedInAgentToolProfilesYAML(
		filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "host", "agent-tool-profiles.yaml"),
	)
	contractcheck.FailErr(t, "codesReferencedInAgentToolProfilesYAML failed", err)
	oarCodes, err := codesWithOARAnchor()
	contractcheck.FailErr(t, "codesWithOARAnchor", err)

	var missing []string
	for code, entry := range cfg.HintCodes {
		if strings.TrimSpace(entry.Emit) == "banner" {
			continue
		}
		if entry.Severity == "info" && !strings.HasPrefix(entry.Emit, "guard:") && !strings.HasPrefix(entry.Emit, "rule:") {
			continue
		}
		if scan.HasEmissionSite(code) || ruleCodes[code] || profileCodes[code] || oarCodes[code] {
			continue
		}
		missing = append(missing, code)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf(`hint codes registered but not emitted from production code (add .Format / FormatCoordinatorNudge, rules deny, or coordinator inject ref):

%v

Fix: wire emission via hint registry (see lycaon/AGENTS.md § Coordinator guidance).`, missing)
	}
}

func TestNoOrphanRejectCodes(t *testing.T) {
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
	ruleCodes, err := guidancescan.CodesReferencedInRulesYAML(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "host", "posture-rules"))
	contractcheck.FailErr(t, "scan hint codes referenced in rules YAML", err)
	profileCodes, err := guidancescan.CodesReferencedInAgentToolProfilesYAML(
		filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "host", "agent-tool-profiles.yaml"),
	)
	contractcheck.FailErr(t, "codesReferencedInAgentToolProfilesYAML failed", err)
	oarCodes, err := codesWithOARAnchor()
	contractcheck.FailErr(t, "codesWithOARAnchor", err)

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

	var registryOrphans []string
	for code, entry := range cfg.HintCodes {
		if strings.TrimSpace(entry.Emit) == "banner" {
			continue
		}
		if entry.Severity == "warning" {
			continue
		}
		if scan.HasEmissionSite(code) || ruleCodes[code] || profileCodes[code] || oarCodes[code] {
			continue
		}
		registryOrphans = append(registryOrphans, code)
	}
	sort.Strings(registryOrphans)
	if len(registryOrphans) > 0 {
		t.Fatalf("hint registry entries with no production emit site: %v", registryOrphans)
	}
}

func TestPreventiveRejectEmittersBackedByLiveHints(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	live := make(map[string]bool)
	for _, entry := range cfg.HintCodes {
		if strings.TrimSpace(entry.Category) == "recoverable" {
			live[strings.TrimSpace(entry.Emit)] = true
		}
	}

	var orphaned []string
	for _, emit := range prompts.PreventiveRejectEmitters() {
		if !live[emit] {
			orphaned = append(orphaned, emit)
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Fatalf(`preventiveRejectEmitters names guards with no live recoverable hint: %v

The prompt tool-surface table filters on emit; a rename or removal here silently drops codes from the table.
Fix: update prompts.preventiveRejectEmitters to match the guard's current emit, or remove the stale entry.`, orphaned)
	}
}

// codesWithOARAnchor returns rules emitted directly by the block plane.
func codesWithOARAnchor() (map[string]bool, error) {
	entries, err := hintregistry.ListEffective()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, ent := range entries {
		var probe map[string]any
		if err := yaml.Unmarshal(ent.Body, &probe); err != nil {
			return nil, err
		}
		if anchor, ok := probe["anchor"].(string); ok && strings.TrimSpace(anchor) != "" {
			out[ent.Code] = true
			continue
		}
		if hc, ok := probe["hint_codes"].(map[string]any); ok {
			if body, ok := hc[ent.Code].(map[string]any); ok {
				if anchor, ok := body["anchor"].(string); ok && strings.TrimSpace(anchor) != "" {
					out[ent.Code] = true
				}
			}
		}
	}
	return out, nil
}
