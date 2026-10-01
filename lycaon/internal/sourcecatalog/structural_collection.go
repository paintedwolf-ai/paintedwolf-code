package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"time"
)

func scanStructureRoot(ctx context.Context, root *os.Root, dir string, options structuralScanOptions, emit func(directoryDiscovery) error) error {
	observation := structuralScanObservation(dir, options)
	scanOptions := options
	scanOptions.mark = func(child string) uint64 {
		if child == dir {
			return observation.Invalidation
		}
		return structuralScanMark(options, child)
	}
	err := scanStructure(ctx, root, dir, scanOptions, emit)
	if err == nil {
		return nil
	}
	var pathError *os.PathError
	filesystemFailure := errors.As(err, &pathError) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrInvalid)
	if dir == "." || ctx.Err() != nil || errors.Is(err, errStructuralScanControl) || !filesystemFailure {
		return err
	}
	return emit(structuralScanFailure(observation, err))
}

func (b *structuralBuilder) retainFailedDirectory(ctx context.Context, observation DirectoryObservation) error {
	index, previous, err := b.children(ctx, observation.Path)
	if err != nil {
		return err
	}
	observation.Sequence = structuralObservationSerial.Add(1)
	observation.FirstListed = observation.Sequence
	observation.Entries = previous.Entries
	observation.Observed = time.Now()
	b.finalized = false
	if err := b.directories.UpdateFlags(ctx, observation.Path, directoryDirty|directoryRepair, 0); err != nil {
		return err
	}
	return b.save(ctx, index, observation)
}
