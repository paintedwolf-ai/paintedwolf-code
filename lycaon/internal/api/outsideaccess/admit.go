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

// Admit lets one request reach the outside path rel when a chat of the
// project holds the file there or a chat location covers it. Requests for any
// other root pass through unchanged.
func Admit(ctx context.Context, ledger *sourceledger.Store, p *project.Project, rootID, rel string) (*project.Project, error) {
	if p == nil || rootID == "" || rootID != p.Outside.ID {
		return p, nil
	}
	if ledger == nil || rel == "" || path.Clean(rel) != rel || path.IsAbs(rel) {
		return nil, projectsource.ErrSourcePathDenied
	}
	admitted, err := ledger.OutsidePathAdmitted(ctx, p.ID, rel)
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, projectsource.ErrSourcePathDenied
	}
	return project.WithOutside(p), nil
}
