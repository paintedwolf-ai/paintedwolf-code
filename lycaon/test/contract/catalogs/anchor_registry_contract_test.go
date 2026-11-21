package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadCatalogAnchors(t *testing.T, root string) (ids map[string]bool, hasEnvelope map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var doc struct {
		Anchors []struct {
			ID       string    `yaml:"id"`
			Envelope yaml.Node `yaml:"envelope"`
		} `yaml:"anchors"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("catalog yaml: %v", err)
	}
	ids = map[string]bool{}
	hasEnvelope = map[string]bool{}
	for _, a := range doc.Anchors {
		id := strings.TrimSpace(a.ID)
		if id == "" {
			continue
		}
		ids[id] = true
		hasEnvelope[id] = a.Envelope.Kind != 0
	}
	return ids, hasEnvelope
}
