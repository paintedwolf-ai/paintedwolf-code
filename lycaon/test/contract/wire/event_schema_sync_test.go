package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type eventSchemaDoc struct {
	Title string `json:"title"`
	Ref   string `json:"$ref"`
}

type eventTopicVocab struct {
	Values []struct {
		ID      string `yaml:"id"`
		Payload string `yaml:"payload"`
	} `yaml:"values"`
}

// TestEventSchemaInventoryIsGeneratedFromTopicVocabulary checks topic closure.
func TestEventSchemaInventoryIsGeneratedFromTopicVocabulary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	vocabData, err := os.ReadFile(filepath.Join(root, "docs", "openapi", "vocab", "EventTopic.yaml"))
	contractcheck.FailErr(t, "read EventTopic vocabulary", err)
	var vocab eventTopicVocab
	contractcheck.FailErr(t, "decode EventTopic vocabulary", yaml.Unmarshal(vocabData, &vocab))

	dir := filepath.Join(root, "docs", "schemas", "events")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read event schema dir", err)

	wantFiles := []string{"envelope.json", "event-topic.generated.json", "session-status.generated.json"}
	for _, topic := range vocab.Values {
		wantFiles = append(wantFiles, topic.ID+".json")
		data, readErr := os.ReadFile(filepath.Join(dir, topic.ID+".json"))
		contractcheck.FailErr(t, "read generated event payload schema", readErr)
		var schema eventSchemaDoc
		contractcheck.FailErr(t, "decode generated event payload schema", json.Unmarshal(data, &schema))
		marker := "#/components/schemas/"
		idx := strings.LastIndex(topic.Payload, marker)
		if idx < 0 {
			t.Fatalf("topic %s has invalid payload ref %q", topic.ID, topic.Payload)
		}
		wantTitle := topic.Payload[idx+len(marker):]
		wantRef := "../../openapi/components/schemas/" + topic.Payload
		if schema.Title != wantTitle || schema.Ref != wantRef {
			t.Errorf("%s.json = title %q ref %q, want %q and %q", topic.ID, schema.Title, schema.Ref, wantTitle, wantRef)
		}
	}
	var gotFiles []string
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".json") {
			gotFiles = append(gotFiles, ent.Name())
		}
	}
	sort.Strings(wantFiles)
	sort.Strings(gotFiles)
	contractcheck.FailSetEqual(t, "generated event schema inventory", wantFiles, gotFiles)
}
