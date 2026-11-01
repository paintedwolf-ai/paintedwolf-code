package nativegen

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/modelstore"
)

// Repair releases the reader before replacing and loading the packaged revision.
func (p *Runtime) Repair(ctx context.Context) (err error) {
	ctx, release, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := p.validate(); err != nil {
		return err
	}
	if p.config.SeedDirectory == "" {
		return errors.New("native generation: packaged model seed is unavailable")
	}
	p.publishReadiness("repairing", "none")
	defer func() { p.settleReadiness(err) }()
	p.unload()
	target := modelstore.Slot{Model: DefaultModelID, Device: p.config.Backend}
	if err := p.config.ModelStore.RepairBundle(ctx, p.config.SeedDirectory, target); err != nil {
		return err
	}
	return p.ensureLoaded(ctx, DefaultModelID)
}
