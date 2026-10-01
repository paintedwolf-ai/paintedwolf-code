package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestStockPolicyCopyUsesFrozenBindings fails when a pack copy member (or
// x-paintedwolf-message) carries a construct outside Appendix C or names a fact
// this engine does not declare.
func TestStockPolicyCopyUsesFrozenBindings(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") || !strings.Contains(path, "/policy/") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var doc map[string]any
		if uErr := yaml.Unmarshal(raw, &doc); uErr != nil {
			t.Errorf("%s: %v", path, uErr)
			return nil
		}
		if _, isRule := doc["oar"]; !isRule {
			return nil // Host presentation entries use the separate guidance renderer.
		}
		rel, _ := filepath.Rel(root, path)
		for _, field := range copyTexts(doc) {
			if !strings.Contains(field, "{{") && !strings.Contains(field, "{%") {
				continue
			}
			bindings, pErr := oarcore.ParseCopy(field)
			if pErr != nil {
				t.Errorf("%s: illegal copy construct %s", rel, pErr.Error())
				continue
			}
			for _, b := range bindings {
				if !oar.DeclaresCopyFact(b.Name) {
					t.Errorf("%s: undeclared copy fact %s", rel, b.Name)
				}
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk policy", err)
}

func copyTexts(doc map[string]any) []string {
	var out []string
	add := func(v any) {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if copy, ok := doc["copy"].(map[string]any); ok {
		for _, v := range copy {
			add(v)
		}
	}
	for _, key := range []string{"what", "cause", "why", "fix", "instead", "message", "x-paintedwolf-message"} {
		add(doc[key])
	}
	return out
}
