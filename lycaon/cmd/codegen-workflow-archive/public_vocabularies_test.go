package main

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func publicVocabularyFixture() treeRelease {
	return treeRelease{
		packsRoot + "/painted-wolf/platform/tools/native-tools.yaml":         []byte("native:\n  files: [read]\nhost_produced_tools: [observe]\nruntime_registered_prefixes: [mcp_]\n"),
		"schemas/workflow_vocabulary.json":                                   []byte(`{"entries":[{"id":"complete","layer":"core","domain":"workflow","parameterized":false}]}`),
		packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml":       []byte("schema_version: '2.0'\nanchors:\n  - id: gate.blocked\n"),
		packsRoot + "/painted-wolf/themes/contributions/themes/example.yaml": []byte("id: painted-wolf/themes:example\n"),
		packsRoot + "/painted-wolf/platform/tools/schemas/extra.yaml":        []byte("schema: {}\n"),
	}
}

func TestPublicVocabularyUsesCatalogIdentities(t *testing.T) {
	source := publicVocabularyFixture()
	vocabulary, err := publicVocabularies(source)
	testutil.FailErr(t, "read public vocabulary", err)
	for kind, names := range map[string][]string{"tools": {"read", "observe", "mcp_*", "extra"}, "anchors": {"gate.blocked"}, "themes": {"painted-wolf/themes:example"}, "workflow_terms": {"complete"}} {
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
		if err := checkVocabulary(kind, vocabulary[kind], after, map[string][]string{}, true); err == nil {
			t.Errorf("%s removal passed", kind)
		}
	}
	for _, file := range []string{"schemas/workflow_vocabulary.json", packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml"} {
		raw := source[file]
		source[file] = []byte("invalid")
		if _, err := publicVocabularies(source); err == nil {
			t.Errorf("invalid %s passed", file)
		}
		source[file] = raw
	}
}
