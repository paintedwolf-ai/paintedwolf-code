package sourcecatalog

import (
	"context"
	"time"
)

// A failed listing preserves known children and settles readers with a visible error.
func (s *indexStore) recordObservationFailure(ctx context.Context, failed DirectoryObservation, cause error) error {
	if failed.Path == "" {
		return nil
	}
	pin, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return err
	}
	defer pin.Release()
	builder, err := newStructuralBuilder(s, pin.value)
	if err != nil {
		return err
	}
	defer builder.close()
	index, previous, err := builder.children(ctx, failed.Path)
	if err != nil {
		return err
	}
	if previous.Sequence != failed.baseSequence {
		return errObservationChanged
	}
	failed.Sequence = structuralObservationSerial.Add(1)
	failed.FirstListed = failed.Sequence
	failed.Entries = previous.Entries
	failed.Observed = time.Now()
	message := cause.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	failed.Failure = message
	failed.Complete = false
	if err := builder.save(ctx, index, failed); err != nil {
		return err
	}
	if err := builder.directories.UpdateFlags(ctx, failed.Path, directoryDirty|directoryRepair, 0); err != nil {
		return err
	}
	return s.publishStructure(ctx, builder, pin.Generation)
}

func DirectoryAncillary(state DirectoryState, children int64) string {
	if state.Failure != "" {
		return "error"
	}
	if !state.Complete && children == 0 {
		return "loading"
	}
	if children == 0 {
		return "empty"
	}
	return ""
}
func directoryBodyRows(state DirectoryState, children int64) int64 {
	if DirectoryAncillary(state, children) != "" {
		children++
	}
	return children
}
