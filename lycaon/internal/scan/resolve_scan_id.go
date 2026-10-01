package scan

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// ResolveProjectScanID expands a full UUID or unambiguous short id prefix to the
// stored scan id for the session project. Empty input returns ("", nil).
func ResolveProjectScanID(ctx context.Context, coord ScanCoordinator, projectDir, scanID string) (string, error) {
	if coord == nil {
		return "", fmt.Errorf("scan coordinator not configured")
	}
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return "", nil
	}
	if len(scanID) < 8 || len(scanID) > 36 {
		return "", nil
	}
	canonical, err := CanonicalPath(projectDir)
	if err != nil {
		return "", err
	}
	return coord.ResolveID(ctx, canonical, scanID)
}

// ResolveID finds at most two indexed ids without loading scan evidence.
func (c *CoordinatorImpl) ResolveID(ctx context.Context, canonicalPath, prefix string) (string, error) {
	if c == nil || c.Store == nil {
		return "", nil
	}
	ids, err := c.Store.queries.ResolveCodeScanIDsByPrefix(ctx, db.ResolveCodeScanIDsByPrefixParams{
		CanonicalPath: canonicalPath,
		Prefix:        prefix,
		PrefixEnd:     prefix + "~",
	})
	if err != nil {
		return "", err
	}
	var match string
	for _, id := range ids {
		if id == prefix {
			return id, nil
		}
		if match != "" {
			return "", nil
		}
		match = id
	}
	return match, nil
}
