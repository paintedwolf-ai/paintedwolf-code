package sourceapi

import (
	"context"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type comparisonProjection interface {
	Extent(context.Context) (int64, error)
	Locate(context.Context, int) (int64, sourcecomparison.ComparisonAnchor, error)
	Frame(context.Context, int64, int) ([]sourcecomparison.ProjectedRow, int64, error)
}

// A replaced projection stays charged until its last in-flight read finishes.
type sourceViewProjection struct {
	comparisonProjection
	charge      *pagedview.Reservation
	users       atomic.Int64
	closeSource func()
}

func (p *sourceViewProjection) retain() { p.users.Add(1) }
func (p *sourceViewProjection) release() {
	if p != nil && p.users.Add(-1) == 0 {
		if p.charge != nil {
			p.charge.Close()
		}
		if p.closeSource != nil {
			p.closeSource()
		}
	}
}

func (view *sourceView) comparisonProjection(ctx context.Context, document *sourcecomparison.Document, intent wire.SourceComparisonIntent, before, after *wire.SecretScreen) (*sourceViewProjection, error) {
	folds := make([]sourcecomparison.Fold, len(intent.Expanded))
	for i, fold := range intent.Expanded {
		folds[i] = sourcecomparison.Fold{Start: fold.Start, End: fold.End}
	}
	intentBytes := int64(cap(intent.Expanded)) * 16
	var charge *pagedview.Reservation
	reserve := func(bytes int64) error {
		var err error
		charge, err = view.comparisonData.comparisonBudget.ReserveEvictingIdle(bytes + intentBytes)
		return err
	}
	projection, err := document.Project(ctx, intent.Mode, folds, reserve)
	if err != nil {
		if charge != nil {
			charge.Close()
		}
		return nil, err
	}
	projection = projection.WithSecretScreens(before, after)
	if err := charge.Resize(projection.RetainedBytes() + intentBytes); err != nil {
		charge.Close()
		return nil, err
	}
	result := &sourceViewProjection{comparisonProjection: projection, charge: charge}
	result.users.Store(1)
	return result, nil
}
