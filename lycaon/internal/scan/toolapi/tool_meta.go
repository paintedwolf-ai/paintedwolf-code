package toolapi

import (
	"sort"
	"strings"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// Host facts the scan handlers and their rejections need. Descriptions and
// argument schemas live in config/packs/painted-wolf/platform/tools/schemas/.

// RegistryScannerIDs returns sorted scanner IDs from the registry (empty filter = all).
func RegistryScannerIDs(reg scanbase.CodeScannerRegistry) []string {
	if reg == nil {
		return nil
	}
	metas := reg.List()
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// RegistryPackCategories returns sorted category names that have at least one host engine.
func RegistryPackCategories(reg scanbase.CodeScannerRegistry) []string {
	if reg == nil {
		return nil
	}
	seen := make(map[api.ScanCategory]bool)
	for _, meta := range reg.List() {
		for _, c := range meta.Categories {
			seen[c] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}
