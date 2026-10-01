package project

import (
	"context"

	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoverBinding adapts Registry to visual.CoverBinding.
type CoverBinding struct {
	Registry Registry
}

var _ visual.CoverBinding = CoverBinding{}

func (b CoverBinding) GetCover(ctx context.Context, projectID string) (visual.Cover, bool, error) {
	if b.Registry == nil {
		return visual.Cover{}, false, nil
	}
	p, err := b.Registry.Get(ctx, projectID)
	if err != nil {
		return visual.Cover{}, false, err
	}
	if p == nil || p.CoverArtifactID == "" {
		return visual.Cover{}, false, nil
	}
	cover := visual.Cover{
		ArtifactID:    p.CoverArtifactID,
		RootSessionID: p.CoverRootSessionID,
		Source:        api.VisualArtifactSource(p.CoverSource),
	}
	if p.CoverUpdatedAt != nil {
		cover.UpdatedAt = *p.CoverUpdatedAt
	}
	return cover, true, nil
}

func (b CoverBinding) SetCover(ctx context.Context, projectID string, cover visual.Cover) error {
	if b.Registry == nil {
		return nil
	}
	_, err := b.Registry.SetCover(ctx, projectID, cover.ArtifactID, cover.RootSessionID, string(cover.Source), cover.UpdatedAt)
	return err
}
