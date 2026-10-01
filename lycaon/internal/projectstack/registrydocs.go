package projectstack

import (
	"strings"

	"github.com/lycaon/lycaon/config"
)

// LoadRegistryDocTemplates reads the per-ecosystem registry doc URL templates
// from config/packs/painted-wolf/platform/host/stack-registry-docs.yaml. It
// returns nil when the file is missing or unparsable, which disables derivation.
func LoadRegistryDocTemplates() map[string]string {
	data, err := config.Read(config.StackRegistryDocs)
	if err != nil {
		return nil
	}
	var raw struct {
		Ecosystems map[string]string `yaml:"ecosystems"`
	}
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil
	}
	return raw.Ecosystems
}

// RegistryDocURLs derives registry documentation pages for manifest deps —
// deterministic, keyless pre-load targets that need no Summarizer call.
// "{name}" in a template is replaced with the dependency name exactly as the
// manifest declares it (Go module paths keep their slashes).
func RegistryDocURLs(templates map[string]string, deps []Dep) []string {
	if len(templates) == 0 || len(deps) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, dep := range deps {
		tpl := templates[dep.Ecosystem]
		if tpl == "" || dep.Name == "" {
			continue
		}
		u := strings.ReplaceAll(tpl, "{name}", dep.Name)
		if !docURLAllowed(u) {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}
