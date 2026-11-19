// Package outsideaccess resolves requests for held outside files.
package outsideaccess

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type Address struct {
	RootID, Path, FileID string
}

// Resolve admits outside reads only by an identity held in this project.
func Resolve(ctx context.Context, ledger *sourceledger.Store, p *project.Project, target Address) (*project.Project, Address, error) {
	if target.FileID == "" {
		if p.Outside.ID != "" && target.RootID == p.Outside.ID {
			return nil, Address{}, projectsource.ErrSourcePathDenied
		}
		return p, target, nil
	}
	if uuid.Validate(target.FileID) != nil || target.RootID != "" || target.Path != "" {
		return nil, Address{}, projectsource.ErrSourcePathInvalid
	}
	root, path, err := ledger.HeldFileAddress(ctx, p.ID, target.FileID)
	if err != nil {
		return nil, Address{}, sourceError(err)
	}
	target.RootID, target.Path = root, path
	if root == p.Outside.ID {
		binding, err := ledger.OutsideReadBinding(ctx, p.ID, target.FileID, path)
		if err != nil {
			return nil, Address{}, sourceError(err)
		}
		p = project.WithOutsideRead(p, binding)
	}
	return p, target, nil
}

func sourceError(err error) error {
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return projectsource.ErrSourceNotFound
	}
	return err
}
