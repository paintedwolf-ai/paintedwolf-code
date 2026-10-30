package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/repochange"
)

func collectDirectoryObservation(ctx context.Context, store *indexStore, dir string, cached DirectoryObservation, held *os.File, demand func() int, consume func(directoryDiscovery) error) (DirectoryObservation, bool, error) {
	observation := DirectoryObservation{Path: dir, baseSequence: cached.Sequence, Invalidation: store.observationMark(dir), Epoch: repochange.CurrentEpoch(store.root.Path)}
	root, release, err := store.navigation.Acquire(ctx, store.root.Path)
	if err != nil {
		return observation, false, err
	}
	defer release()
	if err := validateObservationRoot(root, store.root.Path); err != nil {
		return observation, false, err
	}
	directory, err := openObservationDirectory(ctx, store, root, dir, held)
	if err != nil {
		return observation, false, err
	}
	defer directory.close()
	observation.Stamp = directory.stamp
	// Unwatched membership is reusable only after its directory has been re-statted.
	if cached.Complete && cached.Failure == "" && cached.Stamp.known() && cached.Stamp == observation.Stamp && cached.Invalidation == observation.Invalidation && cached.Epoch == observation.Epoch {
		return cached, true, nil
	}
	reader := directoryBatch{file: directory.entries}
	for {
		finish, err := store.stores.broker.Acquire(ctx, store.observationRequest(dir))
		if err != nil {
			return observation, false, err
		}
		quantum := indexBatchSize
		if maximum := demand(); maximum > 0 {
			quantum = min(quantum, maximum-observation.Entries)
		}
		if quantum <= 0 {
			finish()
			break
		}
		batch, complete, readErr := reader.read(quantum)
		var nodes []indexNode
		if readErr == nil {
			nodes = readObservationNodes(root, directory, dir, batch)
		}
		finish()
		if readErr != nil {
			return observation, false, readErr
		}
		observation.Entries += len(batch)
		observation.Complete = complete
		if err := consume(directoryDiscovery{nodes: nodes, observation: observation}); err != nil {
			return observation, false, err
		}
		if complete {
			break
		}
	}
	return observation, false, nil
}

func openObservationDirectory(ctx context.Context, store *indexStore, root *os.Root, dir string, held *os.File) (*observationDirectory, error) {
	release, err := store.stores.broker.Acquire(ctx, store.observationRequest(dir))
	if err != nil {
		return nil, err
	}
	defer release()
	if held == nil {
		return openConfinedDirectory(root, dir)
	}
	info, err := held.Stat()
	if err != nil {
		return nil, err
	}
	entries, err := directoryKinds(held)
	if err != nil {
		return nil, err
	}
	return &observationDirectory{entries: entries, resolved: dir, stamp: directoryStampOf(info), privateFilter: repochange.NewPrivateDirectoryFilter(filepath.Join(root.Name(), filepath.FromSlash(dir)))}, nil
}

func validateObservationRoot(root *os.Root, path string) error {
	current, err := os.Stat(path)
	if err != nil {
		return err
	}
	held, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(current, held) {
		return errObservationChanged
	}
	return nil
}
