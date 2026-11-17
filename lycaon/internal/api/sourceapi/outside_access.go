package sourceapi

import (
	"context"
	"path"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// admitOutside lets one request read the outside file at rel when a chat of
// the project holds it. Requests for any other root pass through unchanged.
func admitOutside(ctx context.Context, ledger *sourceledger.Store, p *project.Project, rootID, rel string) (*project.Project, error) {
	if p == nil || rootID == "" || rootID != p.Outside.ID {
		return p, nil
	}
	if ledger == nil || rel == "" || path.Clean(rel) != rel || path.IsAbs(rel) {
		return nil, projectsource.ErrSourcePathDenied
	}
	held, err := ledger.OutsideFileHeld(ctx, p.ID, rel)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, projectsource.ErrSourcePathDenied
	}
	return project.WithOutside(p), nil
}
