package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuredCloudCatalogCoversEveryReviewedRule(t *testing.T) {
	t.Parallel()
	paths, err := resolveModulePaths(filepath.Join("..", ".."))
	testutil.FailErr(t, "resolve module paths", err)
	catalog, err := loadStructuredCloudCatalog(paths.structuredCloudMap)
	testutil.FailErr(t, "load structured cloud catalog", err)
	loaded, err := shippedDetectionCatalog()
	testutil.FailErr(t, "load detection catalog", err)

	for _, provider := range catalog.Providers {
		source, ok := loaded.PackByID(provider.SourcePack)
		if !ok {
			t.Fatalf("missing source pack %s", provider.SourcePack)
		}
		resolved, resolveErr := resolveStructuredCloudRules(provider, source)
		testutil.FailErr(t, "resolve "+provider.ID+" mappings", resolveErr)
		if len(resolved) != len(source.Rules) {
			t.Fatalf("%s resolved=%d source=%d", provider.ID, len(resolved), len(source.Rules))
		}
	}
}

func TestStructuredCloudCatalogRejectsMissingReviewedRule(t *testing.T) {
	t.Parallel()
	paths, err := resolveModulePaths(filepath.Join("..", ".."))
	testutil.FailErr(t, "resolve module paths", err)
	catalog, err := loadStructuredCloudCatalog(paths.structuredCloudMap)
	testutil.FailErr(t, "load structured cloud catalog", err)
	loaded, err := shippedDetectionCatalog()
	testutil.FailErr(t, "load detection catalog", err)
	provider := catalog.Providers[0]
	provider.Rules = provider.Rules[:len(provider.Rules)-1]
	source, _ := loaded.PackByID(provider.SourcePack)
	_, err = resolveStructuredCloudRules(provider, source)
	if err == nil || !strings.Contains(err.Error(), "has no structured operation mapping") {
		t.Fatalf("missing mapping error=%v", err)
	}
}

func TestStructuredCloudOutputDeterministic(t *testing.T) {
	t.Parallel()
	paths, err := resolveModulePaths(filepath.Join("..", ".."))
	testutil.FailErr(t, "resolve module paths", err)
	first, second := t.TempDir(), t.TempDir()
	results, err := generateStructuredCloudPacks(paths.structuredCloudMap, first)
	testutil.FailErr(t, "generate first structured packs", err)
	_, err = generateStructuredCloudPacks(paths.structuredCloudMap, second)
	testutil.FailErr(t, "generate second structured packs", err)
	for _, result := range results {
		rel := filepath.Join("config", "packs", "painted-wolf", "security", "host", "detection-packs", result.PackID)
		firstFiles, listErr := listGeneratedFiles(filepath.Join(first, rel))
		testutil.FailErr(t, "list first generated pack", listErr)
		secondFiles, listErr := listGeneratedFiles(filepath.Join(second, rel))
		testutil.FailErr(t, "list second generated pack", listErr)
		if strings.Join(firstFiles, "\x00") != strings.Join(secondFiles, "\x00") {
			t.Fatalf("%s file sets differ", result.PackID)
		}
		for _, file := range firstFiles {
			left, readErr := readFile(filepath.Join(first, rel, file))
			testutil.FailErr(t, "read first generated file", readErr)
			right, readErr := readFile(filepath.Join(second, rel, file))
			testutil.FailErr(t, "read second generated file", readErr)
			if !bytes.Equal(left, right) {
				t.Fatalf("%s/%s differs", result.PackID, file)
			}
		}
	}
}

func TestStructuredCloudGeneratedRulesPreserveReviewedPolicy(t *testing.T) {
	t.Parallel()
	paths, err := resolveModulePaths(filepath.Join("..", ".."))
	testutil.FailErr(t, "resolve module paths", err)
	tmp := t.TempDir()
	_, err = generateStructuredCloudPacks(paths.structuredCloudMap, tmp)
	testutil.FailErr(t, "generate structured cloud packs", err)

	catalog, err := loadStructuredCloudCatalog(paths.structuredCloudMap)
	testutil.FailErr(t, "load structured cloud catalog", err)
	loaded, err := shippedDetectionCatalog()
	testutil.FailErr(t, "load detection catalog", err)
	for _, provider := range catalog.Providers {
		source, _ := loaded.PackByID(provider.SourcePack)
		for _, sourceRule := range source.Rules {
			path := filepath.Join(tmp, "config", "packs", "painted-wolf", "security", "host", "detection-packs", provider.OutputPack, "rules", sourceRule.Slug+".yml")
			body, readErr := readFile(path)
			testutil.FailErr(t, "read generated rule", readErr)
			generated, parseErr := detectionpack.ParseRule(body)
			testutil.FailErr(t, "parse generated rule", parseErr)
			if !generated.Supported || generated.Level != sourceRule.Level || strings.Join(generated.Tags, "\x00") != strings.Join(sourceRule.Tags, "\x00") {
				t.Fatalf("%s/%s policy drift: source=%+v generated=%+v", provider.ID, sourceRule.Slug, sourceRule, generated)
			}
		}
	}
}

func TestStructuredCloudOperationRequiresOperationBearingKey(t *testing.T) {
	t.Parallel()
	paths, err := resolveModulePaths(filepath.Join("..", ".."))
	testutil.FailErr(t, "resolve module paths", err)
	tmp := t.TempDir()
	_, err = generateStructuredCloudPacks(paths.structuredCloudMap, tmp)
	testutil.FailErr(t, "generate structured cloud packs", err)
	path := filepath.Join(tmp, "config", "packs", "painted-wolf", "security", "host", "detection-packs", "gcp-structured-actions", "rules", "credential-mint.yml")
	body, err := readFile(path)
	testutil.FailErr(t, "read credential rule", err)
	rule, err := detectionpack.ParseRule(body)
	testutil.FailErr(t, "parse credential rule", err)
	base := detectionpack.ActionObservation{Tool: "mcp_gcp_call", EffectReach: detectionpack.EffectReachRemote}
	base.ToolArgs = []string{"action=iam.serviceAccountKeys.create"}
	if !rule.Matches(detectionpack.NewEvent(base)) {
		t.Fatal("canonical action argument did not match")
	}
	base.ToolArgs = []string{"note=iam.serviceAccountKeys.create"}
	if rule.Matches(detectionpack.NewEvent(base)) {
		t.Fatal("unrelated argument value matched structured operation")
	}
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path) // #nosec G304 -- test-generated path
}
