package releasecontract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func publicVocabularyFixture() treeRelease {
	return treeRelease{
		packsRoot + "/painted-wolf/platform/tools/native-tools.yaml": []byte("native:\n  files: [read, copy]\nhost_produced_tools: [observe]\nruntime_registered_prefixes: [mcp_]\n"),
		dispositionsPath:                   []byte(`{"tools":[{"tool":"copy","disposition":"replace-routine-schema"},{"tool":"read","disposition":"retain-domain-structured"}]}`),
		"schemas/workflow_vocabulary.json": []byte(`{"entries":[{"id":"complete","layer":"core","domain":"workflow","parameterized":false}]}`),
		packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml":       []byte("schema_version: '2.0'\nanchors:\n  - id: gate.blocked\n"),
		packsRoot + "/painted-wolf/themes/contributions/themes/example.yaml": []byte("id: painted-wolf/themes:example\n"),
		packsRoot + "/painted-wolf/platform/tools/schemas/extra.yaml":        []byte("schema: {}\n"),
		themeUnitSchemaPath: []byte(`{"properties":{"id":{"type":"string"},"tokens":{"type":"object","properties":{"accent":{"type":"string"}}}}}`),
		projectRootsPath:    []byte("package projectroot\n\nconst (\n\tVirtualScratchLabel = \"scratch\"\n\tmaxRoots = 8\n)\n"),
	}
}

func TestPublicVocabularyUsesCatalogIdentities(t *testing.T) {
	source := publicVocabularyFixture()
	vocabulary, err := publicVocabularies(source)
	testutil.FailErr(t, "read public vocabulary", err)
	if _, ok := vocabulary["tools"]["copy"]; ok {
		t.Error("command-only operation remained in public tool vocabulary")
	}
	for kind, names := range map[string][]string{"tools": {"read", "observe", "mcp_*", "extra"}, "anchors": {"gate.blocked"}, "themes": {"painted-wolf/themes:example"}, "workflow_terms": {"complete"}, "theme_keys": {"tokens/accent", "tokens", "id"}, "virtual_roots": {"scratch"}} {
		for _, name := range names {
			if _, ok := vocabulary[kind][name]; !ok {
				t.Errorf("missing %s %s", kind, name)
			}
		}
		after := map[string]string{}
		for name, signature := range vocabulary[kind] {
			after[name] = signature
		}
		delete(after, names[0])
		if err := checkVocabulary(kind, vocabulary[kind], after, map[string]retirementSuccessors{}, nil); err == nil {
			t.Errorf("%s removal passed", kind)
		}
	}
	retyped := map[string]string{}
	for key, signature := range vocabulary["theme_keys"] {
		retyped[key] = signature
	}
	retyped["tokens/accent"] = `"number"`
	if err := checkVocabulary("theme_keys", vocabulary["theme_keys"], retyped, map[string]retirementSuccessors{}, nil); err == nil {
		t.Error("retyped theme key passed")
	}
	for _, file := range []string{"schemas/workflow_vocabulary.json", packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml", packsRoot + "/painted-wolf/platform/tools/native-tools.yaml", dispositionsPath, packsRoot + "/painted-wolf/themes/contributions/themes/example.yaml", themeUnitSchemaPath, projectRootsPath} {
		raw := source[file]
		source[file] = []byte("invalid")
		if _, err := publicVocabularies(source); err == nil {
			t.Errorf("invalid %s passed", file)
		}
		source[file] = raw
	}
}

func TestPublicVocabularyRefusesMissingCatalogsAndDuplicateIdentifiers(t *testing.T) {
	for _, file := range []string{"schemas/workflow_vocabulary.json", packsRoot + "/painted-wolf/platform/tools/native-tools.yaml", packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml", themeUnitSchemaPath, projectRootsPath} {
		source := publicVocabularyFixture()
		delete(source, file)
		if _, err := publicVocabularies(source); err == nil {
			t.Errorf("missing %s passed", file)
		}
	}
	for file, raw := range map[string]string{
		"schemas/workflow_vocabulary.json":                                   `{"entries":[{"id":"same"},{"id":"same"}]}`,
		packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml":       "anchors: [{id: same}, {id: same}]",
		packsRoot + "/painted-wolf/themes/contributions/themes/example.yaml": "name: Missing ID",
		themeUnitSchemaPath: `{"properties":{}}`,
		projectRootsPath:    "package projectroot\n\nconst maxRoots = 8\n",
	} {
		source := publicVocabularyFixture()
		source[file] = []byte(raw)
		if _, err := publicVocabularies(source); err == nil {
			t.Errorf("invalid identities in %s passed", file)
		}
	}
}
