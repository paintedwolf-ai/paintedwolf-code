// Package outsideaccess admits requests for paths in a project's outside
// namespace.
package outsideaccess

import (
	"context"
	"path"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Admit resolves an outside address held by a chat or open document.
func Admit(ctx context.Context, ledger *sourceledger.Store, p *project.Project, rootID, rel string) (*project.Project, error) {
	if p == nil || rootID == "" || rootID != p.Outside.ID {
		return p, nil
	}
	if ledger == nil || rel == "" || path.Clean(rel) != rel || path.IsAbs(rel) {
		return nil, projectsource.ErrSourcePathDenied
	}
	admitted, err := ledger.OutsideFileHeld(ctx, p.ID, rel)
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, projectsource.ErrSourcePathDenied
	}
	return project.WithOutside(p), nil
}
