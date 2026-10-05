package contract

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type goldenField struct {
	path        string
	fieldType   string
	requirement string
	defaultValue string
}

func parseGoldenFile(t *testing.T, path string) ([]goldenField, map[string]string) {
	t.Helper()
	f, err := os.Open(path)
	contractcheck.FailErr(t, "open golden file "+path, err)
	defer f.Close()

	var fields []goldenField
	headers := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			comment := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			parts := strings.SplitN(comment, ":", 2)
			if len(parts) == 2 {
				headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
			continue
		}
		tokens := strings.Fields(line)
		if len(tokens) < 3 {
			t.Fatalf("malformed golden line in %s: %q", path, line)
		}
		gf := goldenField{
			path:        normalizePath(tokens[0]),
			fieldType:   tokens[1],
			requirement: tokens[2],
		}
		for _, tok := range tokens[3:] {
			if strings.HasPrefix(tok, "default=") {
				gf.defaultValue = strings.TrimPrefix(tok, "default=")
			}
		}
		fields = append(fields, gf)
	}
	contractcheck.FailErr(t, "scan golden file "+path, scanner.Err())
	return fields, headers
}

func normalizePath(p string) string {
	return strings.ReplaceAll(p, "[]", "")
}

func TestWorkflowManifestFieldInventoryMatchesGolden(t *testing.T) {
	t.Parallel()
	repoRoot := contractcheck.RepoRoot(t)
	goldenPath := filepath.Join(repoRoot, "lycaon", "test", "contract", "workflows", "testdata", "manifest_fields.golden")

	goldenFields, headers := parseGoldenFile(t, goldenPath)
	if headers["extension_api"] != extpacks.ExtensionAPIVersion {
		t.Fatalf("golden extension_api = %q, want %q", headers["extension_api"], extpacks.ExtensionAPIVersion)
	}
	if headers["format_range"] != "1..1" {
		t.Fatalf("golden format_range = %q, want %q", headers["format_range"], "1..1")
	}

	actualInventory := workflowdef.ManifestFieldInventory()
	actualByPath := make(map[string]workflowdef.ManifestField, len(actualInventory))
	for _, f := range actualInventory {
		actualByPath[f.Path] = f
	}

	goldenByPath := make(map[string]goldenField, len(goldenFields))
	for _, gf := range goldenFields {
		goldenByPath[gf.path] = gf
		actual, ok := actualByPath[gf.path]
		if !ok {
			t.Errorf("manifest field %q in golden file is missing from ManifestFieldInventory()", gf.path)
			continue
		}
		if actual.Type != gf.fieldType {
			t.Errorf("manifest field %q type = %q, golden specifies %q", gf.path, actual.Type, gf.fieldType)
		}
	}

	for _, actual := range actualInventory {
		gf, ok := goldenByPath[actual.Path]
		if !ok {
			t.Errorf("ManifestFieldInventory() contains unrecorded field %q (%s); new fields must be optional and documented in golden file", actual.Path, actual.Type)
			continue
		}
		if gf.requirement != "required" && gf.requirement != "optional" {
			t.Errorf("field %q has invalid requirement %q", gf.path, gf.requirement)
		}
	}
}

func TestExtensionManifestFieldInventoryMatchesGolden(t *testing.T) {
	t.Parallel()
	repoRoot := contractcheck.RepoRoot(t)
	goldenPath := filepath.Join(repoRoot, "lycaon", "test", "contract", "workflows", "testdata", "extension_fields.golden")

	goldenFields, headers := parseGoldenFile(t, goldenPath)
	if headers["extension_api"] != extpacks.ExtensionAPIVersion {
		t.Fatalf("golden extension_api = %q, want %q", headers["extension_api"], extpacks.ExtensionAPIVersion)
	}

	actualInventory := extpacks.ExtensionManifestFieldInventory()
	actualByPath := make(map[string]extpacks.ManifestField, len(actualInventory))
	for _, f := range actualInventory {
		actualByPath[f.Path] = f
	}

	goldenByPath := make(map[string]goldenField, len(goldenFields))
	for _, gf := range goldenFields {
		goldenByPath[gf.path] = gf
		actual, ok := actualByPath[gf.path]
		if !ok {
			t.Errorf("extension field %q in golden file is missing from ExtensionManifestFieldInventory()", gf.path)
			continue
		}
		if actual.Type != gf.fieldType {
			t.Errorf("extension field %q type = %q, golden specifies %q", gf.path, actual.Type, gf.fieldType)
		}
	}

	for _, actual := range actualInventory {
		gf, ok := goldenByPath[actual.Path]
		if !ok {
			t.Errorf("ExtensionManifestFieldInventory() contains unrecorded field %q (%s); new fields must be optional and documented in golden file", actual.Path, actual.Type)
			continue
		}
		if gf.requirement != "required" && gf.requirement != "optional" {
			t.Errorf("extension field %q has invalid requirement %q", gf.path, gf.requirement)
		}
	}
}

func TestArchiveDirectoriesNeverPublishedAsUnits(t *testing.T) {
	t.Parallel()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)

	hasSecuritySurvey := false
	for _, pc := range stock {
		if pc.Manifest.ID == "painted-wolf/security-survey" {
			hasSecuritySurvey = true
		}
		for _, u := range pc.Units {
			pathStr := filepath.ToSlash(u.Path.String())
			if strings.Contains(pathStr, "/archive/") || strings.HasPrefix(pathStr, "archive/") {
				t.Fatalf("pack %q published unit %q from archive path %q", pc.Manifest.ID, u.ID, pathStr)
			}
		}
	}

	if !hasSecuritySurvey {
		t.Fatal("stock packs must contain painted-wolf/security-survey for archive isolation assertion")
	}
}
