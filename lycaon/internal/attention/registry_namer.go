package attention

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
)

// RegistryNamer resolves project display names from the project registry.
type RegistryNamer struct {
	Registry project.Registry
}

// ProjectName returns the project's display name, or "" when the project is
// unnamed or unknown. Callers render their own placeholder.
func (n RegistryNamer) ProjectName(ctx context.Context, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || n.Registry == nil {
		return ""
	}
	p, err := n.Registry.Get(ctx, projectID)
	if err != nil || p == nil {
		return ""
	}
	return strings.TrimSpace(p.Name)
}
