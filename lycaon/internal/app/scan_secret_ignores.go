package app

import (
	"context"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectignore"
)

// Background scans carry root identity. A root shared by multiple projects
// cannot borrow either project's classifier acceptance.
func (b toolWiring) scanSecretIgnores(ctx context.Context, root string) map[string]projectignore.SecretEntry {
	if b.registry == nil || b.secretIgnores == nil || b.secretFingerprinter == nil {
		return nil
	}
	projects, err := b.registry.List(ctx)
	if err != nil {
		return nil
	}
	projectID := ""
	canonical := fspath.CanonicalPath(root)
	for _, p := range projects {
		for _, r := range p.Roots {
			if fspath.CanonicalPath(r.Path) != canonical {
				continue
			}
			if projectID != "" && projectID != p.ID {
				return nil
			}
			projectID = p.ID
		}
	}
	if projectID == "" {
		return nil
	}
	out := map[string]projectignore.SecretEntry{}
	for _, entry := range b.secretIgnores.ActiveValues(ctx, projectID) {
		out[string(b.secretFingerprinter.ScannerFingerprint(entry.Value))] = entry
	}
	return out
}
