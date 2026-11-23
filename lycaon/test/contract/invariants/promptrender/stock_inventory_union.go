package promptrender

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"gopkg.in/yaml.v3"
)

// StockInventoryReport summarizes full-tree classification of lycaon/config/
// across prompt/skill/binding families, OAR/policy/next-steps/anchors/portable-pack
// families, and explicit closed non-prompt schema/manifest/scanner/fixture categories.
type StockInventoryReport struct {
	TotalFiles         int
	AuditedTotal       int
	ExcludedTotal      int
	AuditedByFamily    map[string]int
	ExcludedByCategory map[string]int
	Violations         []string
}

var (
	packBindingRe          = regexp.MustCompile(`^packs/([^/]+/[^/]+)/host/bindings/[^/_][^/]*\.yaml$`)
	packGateFeedbackTmplRe = regexp.MustCompile(`^packs/([^/]+/[^/]+)/guidance/gate-feedback-templates/_gate\.md\.tmpl$`)
	packGateFeedbackRe     = regexp.MustCompile(`^packs/([^/]+/[^/]+)/(?:archive/[^/]+/[^/]+/)?guidance/gate-feedback/[^/_][^/]*\.yaml$`)
	packPolicyRe           = regexp.MustCompile(`^packs/([^/]+/[^/]+)/policy/[^/_][^/]*\.(?:yaml|yml)$`)
	packNextStepsRe        = regexp.MustCompile(`^packs/([^/]+/[^/]+)/host/next-steps/[^/_][^/]*\.(?:yaml|yml)$`)
	packAnchorsRe          = regexp.MustCompile(`^packs/([^/]+/[^/]+)/host/anchors/(?:catalog|oar-profile)\.yaml$`)
	portablePackAssetRe    = regexp.MustCompile(`^oar-portable-pack/(?:[^/]+\.yaml|fixtures/[^/]+\.json)$`)

	goEmbedSourceRe     = regexp.MustCompile(`^(?:config\.go|config_test\.go|paths\.go|rel\.go|configtest/configtest\.go)$`)
	packManifestRe      = regexp.MustCompile(`^(?:packs/painted-wolf/meta\.yaml|packs/[^/]+/[^/]+/extension\.yaml)$`)
	agentManifestRe     = regexp.MustCompile(`^packs/([^/]+/[^/]+)/(?:agents/[^/_][^/]*\.yaml|agents/prompts/_persona-contract\.yaml)$`)
	toolSchemaAssetRe   = regexp.MustCompile(`^packs/([^/]+/[^/]+)/tools/(?:[^/_][^/]*\.yaml|command-language-v1/[^/]+\.json|operation-schemas/[^/]+\.yaml|profiles/[^/]+\.yaml|schemas/[^/]+\.yaml)$`)
	workflowManifestRe  = regexp.MustCompile(`^packs/([^/]+/[^/]+)/(?:workflows/(?:registry\.yaml|_templates/[^/]+\.yaml|_topologies/[^/]+\.yaml|[^/_][^/]*/workflow\.yaml)|archive/[^/]+/[^/]+/(?:SHA256SUMS|workflow\.yaml)|playbooks/[^/]+\.yaml|shared/(?:workflow-diagnostics/[^/]+\.yaml|intake/[^/]+/question\.yaml|survey/[^/]+\.yaml)|guidance/evidence-kinds\.yaml)$`)
	humanNoticeCardRe   = regexp.MustCompile(`^packs/([^/]+/[^/]+)/(?:approvals/[^/]+/explain\.yaml|host/(?:client-notices|user-notices)/[^/]+\.yaml)$`)
	uiContributionRe    = regexp.MustCompile(`^packs/([^/]+/[^/]+)/contributions/(?:commands|editor-actions|keybindings|menus|themes|configuration|mcp-requirements|search-sources|operations)/[^/]+\.yaml$`)
	hostDataSubdirRe    = regexp.MustCompile(`^packs/([^/]+/[^/]+)/host/(?:decision-release\.json|ask-triggers/[^/]+\.yaml|posture-rules/[^/]+\.yaml|detection-pack-upstream/[^/]+\.yaml|detection-packs/.+\.(?:yaml|yml)|secret-mint/(?:[^/]+\.yaml|vendor/zxcvbn-passwords/(?:LICENSE|passwords\.txt))|secret-patterns/[^/]+\.yaml)$`)
	hostDirectYAMLRe    = regexp.MustCompile(`^packs/painted-wolf/[^/]+/host/[^/_][^/]*\.yaml$`)
	skillStaticAssetRe  = regexp.MustCompile(`^packs/[^/]+/[^/]+/skills/[^/]+/assets/[^/]+\.(?:html|svg)$`)
	runtimeAndScannerRe = regexp.MustCompile(`^(?:gitengine/pin\.yaml|runtime/(?:model-role-exclusions\.yaml|ocrengine/pin\.yaml|search/grammar\.json|source/[^/]+\.yaml|storage/[^/]+\.yaml|scanners/(?:advisory-severity\.json|(?:bundled-manifest|gates|ignores|opengrep-gates|rules-provenance|runner|scan-excludes|scan-hints|scanners|security-scanners)\.yaml|opengrep-attestations/[0-9a-f]+\.json|secret-rules/vendor/kingfisher/(?:LICENSE|NOTICE|manifest\.yaml|rules/[^/]+\.yml)|rules/.+)))$`)
	fixtureAssetRe      = regexp.MustCompile(`^fixtures/(?:mock_llm\.yaml|model-policy\.fireworks\.example\.yaml|workflows/[^/]+\.yaml|example-packs/.+|tutorial-pack/.+)$`)
)

type rawBindingDoc struct {
	On         string                   `yaml:"on"`
	Selector   anchor.Selector          `yaml:"selector"`
	Effect     string                   `yaml:"effect"`
	When       string                   `yaml:"when"`
	Render     string                   `yaml:"render"`
	Tier       string                   `yaml:"tier"`
	Invariants anchor.BindingInvariants `yaml:"invariants"`
}

func stockConfigUnionReconciled(r *invariants.Run) {
	configRoot := r.Path("lycaon/config")
	report, err := ReconcileConfigInventory(configRoot)
	r.Must(err)
	r.Expect(report.TotalFiles, "config files")
	for _, v := range report.Violations {
		r.Report("%s", v)
	}
}

// ReconcileConfigInventory walks configRoot and verifies that 100% of files are
// classified into an audited prompt/skill/binding/OAR family or an explicit
// closed non-prompt category, validating binding/template edges and checking
// excluded non-prompt files for leaked executable template directives.
func ReconcileConfigInventory(configRoot string) (StockInventoryReport, error) {
	rep := StockInventoryReport{
		AuditedByFamily:    map[string]int{},
		ExcludedByCategory: map[string]int{},
	}
	var allRels []string
	err := filepath.WalkDir(configRoot, func(full string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, rErr := filepath.Rel(configRoot, full)
		if rErr != nil {
			return rErr
		}
		allRels = append(allRels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return rep, fmt.Errorf("walk %s: %w", configRoot, err)
	}

	guidanceStems := map[string]bool{}
	var bindingRels []string
	hasAnchorCatalog := false
	if _, statErr := os.Stat(filepath.Join(configRoot, filepath.FromSlash(string(config.AnchorCatalog)))); statErr == nil {
		_ = anchorcatalog.InstallBundled()
		hasAnchorCatalog = anchorcatalog.Loaded()
	}

	for _, rel := range allRels {
		rep.TotalFiles++
		srcLabel := "config/" + rel
		full := filepath.Join(configRoot, filepath.FromSlash(rel))

		if fam := classifyAuditedConfigFile(rel, full, srcLabel, guidanceStems, &bindingRels, &rep.Violations); fam != "" {
			rep.AuditedByFamily[fam]++
			rep.AuditedTotal++
			continue
		}
		if cat := classifyExcludedConfigFile(rel, full, srcLabel, &rep.Violations); cat != "" {
			rep.ExcludedByCategory[cat]++
			rep.ExcludedTotal++
			continue
		}
		rep.Violations = append(rep.Violations, fmt.Sprintf("%s: unclassified stock source outside audited prompt/skill/binding/OAR families and explicit closed non-prompt categories", srcLabel))
	}

	if len(guidanceStems) > 0 || hasAnchorCatalog {
		validateDiscoveredBindings(configRoot, bindingRels, guidanceStems, hasAnchorCatalog, &rep.Violations)
	}
	return rep, nil
}

func classifyAuditedConfigFile(
	rel, full, srcLabel string,
	guidanceStems map[string]bool,
	bindingRels *[]string,
	violations *[]string,
) string {
	switch {
	case packAgentPromptRe.MatchString(rel):
		return "ct01/agents/prompts"
	case packPartialRe.MatchString(rel):
		return "ct01/shared/partials"
	case packUnitRe.MatchString(rel):
		return "ct01/shared/units"
	case packArchetypeRe.MatchString(rel):
		return "ct01/shared/archetypes"
	case packGuidanceRe.MatchString(rel):
		if idx := strings.Index(rel, "/guidance/"); idx >= 0 {
			guidanceStems[strings.TrimSuffix(rel[idx+len("/guidance/"):], ".md")] = true
		}
		return "ct01/guidance"
	case packArchiveGuidRe.MatchString(rel):
		return "ct01/archive/guidance"
	case packGateFeedbackTmplRe.MatchString(rel):
		raw, err := os.ReadFile(full)
		if err != nil {
			*violations = append(*violations, fmt.Sprintf("%s: %v", srcLabel, err))
		} else if _, pErr := pongoplain.ParseTemplateTree(string(raw)); pErr != nil {
			*violations = append(*violations, fmt.Sprintf("%s: invalid Pongo template syntax: %v", srcLabel, pErr))
		}
		return "ct01/guidance/gate-feedback-template"
	case packGateFeedbackRe.MatchString(rel):
		return "ct01/guidance/gate-feedback"
	case packSkillBodyRe.MatchString(rel):
		return "ct01/skills/SKILL.md"
	case packSkillRefRe.MatchString(rel):
		return "ct01/skills/references"
	case packBindingRe.MatchString(rel):
		*bindingRels = append(*bindingRels, rel)
		return "ct01/host/bindings"
	case packPolicyRe.MatchString(rel):
		return "ct04/policy"
	case packNextStepsRe.MatchString(rel):
		return "ct04/host/next-steps"
	case packAnchorsRe.MatchString(rel):
		return "ct04/host/anchors"
	case portablePackAssetRe.MatchString(rel):
		return "ct04/oar-portable-pack"
	default:
		return ""
	}
}

func classifyExcludedConfigFile(rel, full, srcLabel string, violations *[]string) string {
	if closedDocExclusions[rel] || scannerDocExcludeRe.MatchString(rel) {
		checkNonPromptTemplateLeak(rel, full, srcLabel, true, violations)
		return "nonprompt/static-docs"
	}
	if strings.HasSuffix(rel, ".md") && !strings.HasPrefix(rel, "fixtures/") {
		// Unclassified .md outside static-docs and fixtures must never enter non-prompt categories.
		return ""
	}
	cat := matchNonPromptCategory(rel)
	if cat == "" {
		return ""
	}
	if cat == "nonprompt/human-ui-notices-and-approvals" {
		checkHumanNoticeNoModelFacts(full, srcLabel, violations)
		return cat
	}
	if cat != "nonprompt/test-fixtures" && cat != "nonprompt/go-embed" && !strings.HasPrefix(rel, "runtime/scanners/rules/vendor/") && !strings.HasPrefix(rel, "runtime/scanners/secret-rules/vendor/") {
		checkNonPromptTemplateLeak(rel, full, srcLabel, false, violations)
	}
	return cat
}

func matchNonPromptCategory(rel string) string {
	switch {
	case goEmbedSourceRe.MatchString(rel):
		return "nonprompt/go-embed"
	case packManifestRe.MatchString(rel):
		return "nonprompt/pack-manifests"
	case agentManifestRe.MatchString(rel):
		return "nonprompt/agent-manifests"
	case toolSchemaAssetRe.MatchString(rel):
		if strings.HasSuffix(rel, ".yaml") && !isConsumedStockPackYAML(rel) {
			return ""
		}
		return "nonprompt/tool-schemas-and-profiles"
	case workflowManifestRe.MatchString(rel):
		if strings.HasSuffix(rel, ".yaml") && !isConsumedStockPackYAML(rel) {
			return ""
		}
		return "nonprompt/workflow-manifests-and-archives"
	case humanNoticeCardRe.MatchString(rel):
		return "nonprompt/human-ui-notices-and-approvals"
	case uiContributionRe.MatchString(rel):
		return "nonprompt/ui-contributions"
	case hostDataSubdirRe.MatchString(rel):
		return "nonprompt/host-catalogs-and-security-data"
	case hostDirectYAMLRe.MatchString(rel) && config.ConsumesPackYAML(config.Rel(rel)):
		return "nonprompt/host-catalogs-and-security-data"
	case skillStaticAssetRe.MatchString(rel):
		return "nonprompt/skill-static-assets"
	case runtimeAndScannerRe.MatchString(rel):
		return "nonprompt/runtime-and-scanners"
	case fixtureAssetRe.MatchString(rel):
		return "nonprompt/test-fixtures"
	default:
		return ""
	}
}

func isConsumedStockPackYAML(rel string) bool {
	trimmed, ok := strings.CutPrefix(rel, "packs/")
	if !ok {
		return false
	}
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 3 {
		return false
	}
	packID := parts[0] + "/" + parts[1]
	packRel := parts[2]
	return extpacks.UnitIDFor(packID, packRel) != "" || config.ConsumesPackYAML(config.Rel(rel))
}

func checkHumanNoticeNoModelFacts(full, srcLabel string, violations *[]string) {
	raw, err := os.ReadFile(full)
	if err != nil {
		return
	}
	text := string(raw)
	if strings.Contains(text, "paintedwolf.next_steps") || strings.Contains(text, "profile_has_") {
		*violations = append(*violations, fmt.Sprintf("%s: human UI notice/approval template references model-turn capability facts", srcLabel))
	}
}

func checkNonPromptTemplateLeak(rel, full, srcLabel string, checkInterpolation bool, violations *[]string) {
	raw, err := os.ReadFile(full)
	if err != nil {
		return
	}
	text := string(raw)
	if strings.Contains(text, "{% if ") || strings.Contains(text, "{% set ") || strings.Contains(text, "{% for ") || strings.Contains(text, "{% include ") {
		*violations = append(*violations, fmt.Sprintf("%s: excluded non-prompt file contains executable Pongo control tags", srcLabel))
	}
	if strings.Contains(text, "paintedwolf.next_steps") && !strings.HasSuffix(rel, "oar-profile.yaml") {
		*violations = append(*violations, fmt.Sprintf("%s: excluded non-prompt file binds paintedwolf.next_steps", srcLabel))
	}
	if checkInterpolation && (strings.Contains(text, "{{") || strings.Contains(text, "{%")) {
		*violations = append(*violations, fmt.Sprintf("%s: excluded static documentation contains Pongo template syntax", srcLabel))
	}
}

func validateDiscoveredBindings(
	configRoot string,
	bindingRels []string,
	guidanceStems map[string]bool,
	hasAnchorCatalog bool,
	violations *[]string,
) {
	for _, rel := range bindingRels {
		srcLabel := "config/" + rel
		raw, err := os.ReadFile(filepath.Join(configRoot, filepath.FromSlash(rel)))
		if err != nil {
			*violations = append(*violations, fmt.Sprintf("%s: %v", srcLabel, err))
			continue
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		var doc rawBindingDoc
		if dErr := dec.Decode(&doc); dErr != nil {
			*violations = append(*violations, fmt.Sprintf("%s: decode host binding: %v", srcLabel, dErr))
			continue
		}
		on := strings.TrimSpace(doc.On)
		if on == "" {
			*violations = append(*violations, fmt.Sprintf("%s: host binding missing on anchor", srcLabel))
		} else if hasAnchorCatalog {
			if aErr := anchorcatalog.Require(on); aErr != nil {
				*violations = append(*violations, fmt.Sprintf("%s: unknown anchor %q: %v", srcLabel, on, aErr))
			}
		}
		if strings.EqualFold(strings.TrimSpace(doc.Effect), "inform") {
			stem := strings.TrimSpace(doc.Render)
			if stem == "" {
				*violations = append(*violations, fmt.Sprintf("%s: inform binding missing render target", srcLabel))
			} else if len(guidanceStems) > 0 && !guidanceStems[stem] {
				*violations = append(*violations, fmt.Sprintf("%s: inform binding render %q resolves to no reconciled guidance template", srcLabel, stem))
			}
		}
		if when := strings.TrimSpace(doc.When); when != "" {
			if _, cErr := oarcore.ConditionReferences(when); cErr != nil {
				*violations = append(*violations, fmt.Sprintf("%s: invalid binding when condition %q: %v", srcLabel, when, cErr))
			}
		}
	}
}
