package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"gopkg.in/yaml.v3"
)

type structuredCloudCatalog struct {
	Version   int                       `yaml:"version"`
	Providers []structuredCloudProvider `yaml:"providers"`
}

type structuredCloudProvider struct {
	ID             string                  `yaml:"id"`
	SourcePack     string                  `yaml:"source_pack"`
	OutputPack     string                  `yaml:"output_pack"`
	Label          string                  `yaml:"label"`
	Description    string                  `yaml:"description"`
	FixtureTool    string                  `yaml:"fixture_tool"`
	FixtureSubject string                  `yaml:"fixture_subject"`
	ReadOperation  string                  `yaml:"read_operation"`
	Sources        []structuredCloudSource `yaml:"sources"`
	Rules          []structuredCloudRule   `yaml:"rules"`
}

type structuredCloudSource struct {
	Name      string `yaml:"name"`
	URL       string `yaml:"url"`
	Retrieved string `yaml:"retrieved"`
	Licence   string `yaml:"licence"`
}

type structuredCloudRule struct {
	SourceRule string                   `yaml:"source_rule"`
	Variants   []structuredCloudVariant `yaml:"variants"`
	ExcludeAny []string                 `yaml:"exclude_any"`
}

type structuredCloudVariant struct {
	Operations  []string                     `yaml:"operations"`
	RequireAll  []structuredCloudRequirement `yaml:"require_all"`
	FixtureArgs map[string]string            `yaml:"fixture_args"`
}

type structuredCloudRequirement struct {
	Any []string `yaml:"any"`
}

type bridgeGenerationResult struct {
	PackID         string
	RuleCount      int
	OperationCount int
}

type structuredCloudResolvedRule struct {
	Source  detectionpack.Rule
	Mapping structuredCloudRule
}

const structuredOperationKeyPattern = `(.*[.])?(action|api_action|operation|operation_name|permission|method)`

var ruleNamespace = uuid.MustParse("a1950003-1953-4000-8000-000000000001")

func generateStructuredCloudPacks(catalogPath, outRoot string) ([]bridgeGenerationResult, error) {
	catalog, err := loadStructuredCloudCatalog(catalogPath)
	if err != nil {
		return nil, err
	}
	loaded, err := shippedDetectionCatalog()
	if err != nil {
		return nil, fmt.Errorf("load reviewed CLI packs: %w", err)
	}
	results := make([]bridgeGenerationResult, 0, len(catalog.Providers))
	for _, provider := range catalog.Providers {
		result, err := generateStructuredCloudPack(provider, loaded, outRoot)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", provider.ID, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func loadStructuredCloudCatalog(path string) (structuredCloudCatalog, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- module-root catalog path
	if err != nil {
		return structuredCloudCatalog{}, fmt.Errorf("read structured cloud catalog: %w", err)
	}
	var catalog structuredCloudCatalog
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		return structuredCloudCatalog{}, fmt.Errorf("parse structured cloud catalog: %w", err)
	}
	if catalog.Version != 1 || len(catalog.Providers) == 0 {
		return structuredCloudCatalog{}, fmt.Errorf("structured cloud catalog version=%d providers=%d", catalog.Version, len(catalog.Providers))
	}
	seen := map[string]struct{}{}
	for i := range catalog.Providers {
		provider := &catalog.Providers[i]
		if err := validateStructuredCloudProvider(*provider); err != nil {
			return structuredCloudCatalog{}, err
		}
		if _, duplicate := seen[provider.OutputPack]; duplicate {
			return structuredCloudCatalog{}, fmt.Errorf("duplicate output pack %q", provider.OutputPack)
		}
		seen[provider.OutputPack] = struct{}{}
	}
	return catalog, nil
}

func validateStructuredCloudProvider(provider structuredCloudProvider) error {
	if provider.ID == "" || provider.SourcePack == "" || provider.OutputPack == "" || provider.Label == "" || provider.Description == "" {
		return fmt.Errorf("incomplete structured cloud provider %q", provider.ID)
	}
	if provider.FixtureTool == "" || provider.FixtureSubject == "" || provider.ReadOperation == "" || len(provider.Sources) == 0 || len(provider.Rules) == 0 {
		return fmt.Errorf("%s requires fixtures, sources, and rules", provider.ID)
	}
	seenRules := map[string]struct{}{}
	seenOperations := map[string]string{}
	for _, rule := range provider.Rules {
		if rule.SourceRule == "" || len(rule.Variants) == 0 {
			return fmt.Errorf("%s has incomplete source rule %q", provider.ID, rule.SourceRule)
		}
		if _, duplicate := seenRules[rule.SourceRule]; duplicate {
			return fmt.Errorf("%s duplicates source rule %q", provider.ID, rule.SourceRule)
		}
		seenRules[rule.SourceRule] = struct{}{}
		for _, pattern := range rule.ExcludeAny {
			if err := validateStructuredRegex(pattern); err != nil {
				return fmt.Errorf("%s/%s exclusion: %w", provider.ID, rule.SourceRule, err)
			}
		}
		for _, variant := range rule.Variants {
			if len(variant.Operations) == 0 {
				return fmt.Errorf("%s/%s has an empty variant", provider.ID, rule.SourceRule)
			}
			for _, operation := range variant.Operations {
				key := strings.ToLower(strings.TrimSpace(operation))
				if key == "" {
					return fmt.Errorf("%s/%s has an empty operation", provider.ID, rule.SourceRule)
				}
				if previous, duplicate := seenOperations[key]; duplicate {
					return fmt.Errorf("%s operation %q belongs to both %s and %s", provider.ID, operation, previous, rule.SourceRule)
				}
				seenOperations[key] = rule.SourceRule
			}
			for _, requirement := range variant.RequireAll {
				if len(requirement.Any) == 0 {
					return fmt.Errorf("%s/%s has an empty requirement", provider.ID, rule.SourceRule)
				}
				for _, pattern := range requirement.Any {
					if err := validateStructuredRegex(pattern); err != nil {
						return fmt.Errorf("%s/%s requirement: %w", provider.ID, rule.SourceRule, err)
					}
				}
			}
			if len(variant.RequireAll) > 0 && len(variant.FixtureArgs) == 0 {
				return fmt.Errorf("%s/%s constrained variant requires fixture_args", provider.ID, rule.SourceRule)
			}
		}
	}
	return nil
}

func validateStructuredRegex(pattern string) error {
	if strings.Contains(pattern, "'") {
		return fmt.Errorf("regex %q cannot contain a single quote", pattern)
	}
	normalized := strings.TrimPrefix(strings.TrimSpace(pattern), "(?i)")
	if !strings.HasPrefix(normalized, "^") || !strings.HasSuffix(normalized, "$") {
		return fmt.Errorf("regex %q must be fully anchored", pattern)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return err
	}
	return nil
}

func generateStructuredCloudPack(provider structuredCloudProvider, catalog *detectionpack.Catalog, outRoot string) (bridgeGenerationResult, error) {
	sourcePack, ok := catalog.PackByID(provider.SourcePack)
	if !ok {
		return bridgeGenerationResult{}, fmt.Errorf("reviewed source pack %q is missing", provider.SourcePack)
	}
	rules, err := resolveStructuredCloudRules(provider, sourcePack)
	if err != nil {
		return bridgeGenerationResult{}, err
	}
	packDir := filepath.Join(outRoot, "config", "packs", "painted-wolf", "security", "host", "detection-packs", provider.OutputPack)
	if err := os.RemoveAll(packDir); err != nil {
		return bridgeGenerationResult{}, err
	}
	if err := writeGeneratedFile(filepath.Join(packDir, "pack.yaml"), renderStructuredCloudPack(provider)); err != nil {
		return bridgeGenerationResult{}, err
	}
	operationCount := 0
	for _, resolved := range rules {
		for _, variant := range resolved.Mapping.Variants {
			operationCount += len(variant.Operations)
		}
		body := renderStructuredCloudRule(provider, resolved)
		if err := writeGeneratedFile(filepath.Join(packDir, "rules", resolved.Source.Slug+".yml"), body); err != nil {
			return bridgeGenerationResult{}, err
		}
	}
	if err := writeGeneratedFile(filepath.Join(packDir, "fixtures.yaml"), renderStructuredCloudFixtures(provider, rules)); err != nil {
		return bridgeGenerationResult{}, err
	}
	return bridgeGenerationResult{PackID: provider.OutputPack, RuleCount: len(rules), OperationCount: operationCount}, nil
}

func resolveStructuredCloudRules(provider structuredCloudProvider, sourcePack detectionpack.Pack) ([]structuredCloudResolvedRule, error) {
	bySlug := make(map[string]detectionpack.Rule, len(sourcePack.Rules))
	for _, rule := range sourcePack.Rules {
		bySlug[rule.Slug] = rule
	}
	resolved := make([]structuredCloudResolvedRule, 0, len(provider.Rules))
	covered := map[string]struct{}{}
	for _, mapping := range provider.Rules {
		source, ok := bySlug[mapping.SourceRule]
		if !ok {
			return nil, fmt.Errorf("mapping names unknown %s rule %q", provider.SourcePack, mapping.SourceRule)
		}
		covered[mapping.SourceRule] = struct{}{}
		resolved = append(resolved, structuredCloudResolvedRule{Source: source, Mapping: mapping})
	}
	for _, rule := range sourcePack.Rules {
		if _, ok := covered[rule.Slug]; !ok {
			return nil, fmt.Errorf("reviewed %s rule %q has no structured operation mapping", provider.SourcePack, rule.Slug)
		}
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Source.Slug < resolved[j].Source.Slug })
	return resolved, nil
}

func renderStructuredCloudPack(provider structuredCloudProvider) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	b.WriteString("id: " + provider.OutputPack + "\n")
	b.WriteString("label: " + provider.Label + "\n")
	b.WriteString("description: >-\n")
	b.WriteString(wrapYAMLBlock(provider.Description, 2))
	b.WriteString("source: generated\n")
	b.WriteString("generated_from:\n")
	for _, source := range provider.Sources {
		b.WriteString("  - name: " + source.Name + "\n")
		b.WriteString("    url: " + source.URL + "\n")
		b.WriteString("    retrieved: '" + source.Retrieved + "'\n")
		b.WriteString("    licence: " + source.Licence + "\n")
	}
	return b.String()
}

func renderStructuredCloudRule(provider structuredCloudProvider, resolved structuredCloudResolvedRule) string {
	var b strings.Builder
	source := resolved.Source
	b.WriteString(generatedHeader)
	b.WriteString("title: " + source.Title + "\n")
	b.WriteString("id: " + stableStructuredRuleID(provider.OutputPack, source.Slug).String() + "\n")
	b.WriteString("status: stable\n")
	b.WriteString("description: >-\n")
	b.WriteString(wrapYAMLBlock(source.Description+" This is the exact structured-operation equivalent of the reviewed "+provider.SourcePack+" rule.", 2))
	b.WriteString("references:\n")
	for _, ref := range structuredCloudReferences(source, provider.Sources) {
		b.WriteString("  - " + ref + "\n")
	}
	b.WriteString("author: Generated from reviewed Painted Wolf rules and official provider operation catalogs\n")
	b.WriteString("date: '" + latestStructuredCloudDate(provider.Sources) + "'\n")
	b.WriteString("modified: '" + latestStructuredCloudDate(provider.Sources) + "'\n")
	b.WriteString("logsource: {product: lycaon, service: tool_exec}\n")
	b.WriteString("detection:\n")
	b.WriteString("  selection_tool: {Tool|startswith: mcp_}\n")
	branches := make([]string, 0, len(resolved.Mapping.Variants))
	for i, variant := range resolved.Mapping.Variants {
		opName := fmt.Sprintf("selection_operation_%d", i)
		b.WriteString("  " + opName + ":\n")
		b.WriteString("    ToolArg|re:\n")
		for _, operation := range variant.Operations {
			b.WriteString("      - '(?i)^" + structuredOperationKeyPattern + "=" + regexp.QuoteMeta(operation) + "$'\n")
		}
		terms := []string{opName}
		for j, requirement := range variant.RequireAll {
			name := fmt.Sprintf("selection_context_%d_%d", i, j)
			b.WriteString("  " + name + ":\n")
			b.WriteString("    ToolArg|re:\n")
			for _, pattern := range requirement.Any {
				b.WriteString("      - '" + pattern + "'\n")
			}
			terms = append(terms, name)
		}
		branches = append(branches, "("+strings.Join(terms, " and ")+")")
	}
	if len(resolved.Mapping.ExcludeAny) > 0 {
		b.WriteString("  filter_excluded_context:\n")
		b.WriteString("    ToolArg|re:\n")
		for _, pattern := range resolved.Mapping.ExcludeAny {
			b.WriteString("      - '" + pattern + "'\n")
		}
	}
	b.WriteString("  filter_rehearsal:\n")
	b.WriteString("    ToolArg|re:\n")
	b.WriteString("      - '(?i)^(.*[.])?(dry_run|dryrun|preview|validate_only|generate_cli_skeleton|what_if)=true$'\n")
	b.WriteString("      - '(?i)^(.*[.])?(mode|execution_mode)=(dry_run|preview|validate|what_if)$'\n")
	b.WriteString("  filter_local: {EffectReach: local}\n")
	condition := "selection_tool and (" + strings.Join(branches, " or ") + ")"
	if len(resolved.Mapping.ExcludeAny) > 0 {
		condition += " and not filter_excluded_context"
	}
	condition += " and not filter_rehearsal and not filter_local"
	b.WriteString("  condition: " + condition + "\n")
	b.WriteString("level: " + string(source.Level) + "\n")
	b.WriteString("tags:\n")
	for _, tag := range source.Tags {
		b.WriteString("  - " + tag + "\n")
	}
	return b.String()
}

func renderStructuredCloudFixtures(provider structuredCloudProvider, rules []structuredCloudResolvedRule) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	b.WriteString("fixtures:\n")
	for _, resolved := range rules {
		variant := resolved.Mapping.Variants[0]
		operation := variant.Operations[0]
		b.WriteString("  - rule: " + resolved.Source.Slug + "\n")
		b.WriteString("    positive:\n")
		writeCloudFixture(&b, provider, operation, variant.FixtureArgs)
		b.WriteString("    negative:\n")
		writeCloudFixture(&b, provider, provider.ReadOperation, nil)
		writeCloudFixture(&b, provider, operation, mergeFixtureArgs(variant.FixtureArgs, map[string]string{"dry_run": "true"}))
		writeCloudFixture(&b, provider, operation, mergeFixtureArgs(variant.FixtureArgs, map[string]string{"endpoint_url": "http://127.0.0.1:4566"}))
		if len(variant.RequireAll) > 0 {
			writeCloudFixture(&b, provider, operation, nil)
		}
	}
	return b.String()
}

func mergeFixtureArgs(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func writeCloudFixture(b *strings.Builder, provider structuredCloudProvider, operation string, args map[string]string) {
	b.WriteString("      - tool: " + provider.FixtureTool + "\n")
	b.WriteString("        approval_category: mcp\n")
	b.WriteString("        approval_subject: " + provider.FixtureSubject + "\n")
	b.WriteString("        args:\n")
	b.WriteString("          action: " + yamlQuote(operation) + "\n")
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString("          " + key + ": " + yamlQuote(args[key]) + "\n")
	}
}

func structuredCloudReferences(source detectionpack.Rule, sources []structuredCloudSource) []string {
	references := append([]string(nil), source.References...)
	for _, item := range sources {
		references = append(references, item.URL)
	}
	sort.Strings(references)
	return dedupeSorted(references)
}

func latestStructuredCloudDate(sources []structuredCloudSource) string {
	latest := ""
	for _, source := range sources {
		if source.Retrieved > latest {
			latest = source.Retrieved
		}
	}
	return latest
}

func stableStructuredRuleID(pack, slug string) uuid.UUID {
	return uuid.NewSHA1(ruleNamespace, []byte(pack+"/"+slug))
}
