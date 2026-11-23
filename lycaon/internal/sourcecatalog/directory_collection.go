package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/repochange"
)

func collectDirectoryObservation(ctx context.Context, store *indexStore, dir string, cached DirectoryObservation, held DirectoryReadAdmission, demand func() int, consume func(directoryDiscovery) error) (DirectoryObservation, bool, error) {
	observation := DirectoryObservation{Path: dir, baseSequence: cached.Sequence, Invalidation: store.observationMark(dir), Epoch: repochange.CurrentEpoch(store.root.Path)}
	root, release, err := acquireObservationRoot(ctx, store)
	if err != nil {
		return observation, false, err
	}
	defer release()
	directory, err := openObservationDirectory(ctx, store, root, dir, held)
	if err != nil {
		return observation, false, err
	}
	defer directory.close()
	observation.Stamp = directory.stamp
	if store.root.Only != "" {
		return observeExactEntry(ctx, store, root, dir, directory, observation, consume)
	}
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
		if held != nil {
			if err := held.AdmitDirectoryRead(); err != nil {
				finish()
				return observation, false, err
			}
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

func observeExactEntry(ctx context.Context, store *indexStore, root *os.Root, dir string, directory *observationDirectory, observation DirectoryObservation, consume func(directoryDiscovery) error) (DirectoryObservation, bool, error) {
	finish, err := store.stores.broker.Acquire(ctx, store.observationRequest(dir))
	if err != nil {
		return observation, false, err
	}
	defer finish()
	name := store.root.Only
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return observation, false, err
	}
	var nodes []indexNode
	if err == nil && info.Mode().IsRegular() {
		nodes = readObservationNodes(root, directory, dir, []directoryEntry{directoryKind{name: name, kind: info.Mode().Type()}})
	}
	observation.Entries, observation.Complete = len(nodes), true
	return observation, false, consume(directoryDiscovery{nodes: nodes, observation: observation})
}

func openObservationDirectory(ctx context.Context, store *indexStore, root *os.Root, dir string, held DirectoryReadAdmission) (*observationDirectory, error) {
	release, err := store.stores.broker.Acquire(ctx, store.observationRequest(dir))
	if err != nil {
		return nil, err
	}
	defer release()
	if held == nil {
		return openConfinedDirectory(root, dir)
	}
	if err := held.AdmitDirectoryRead(); err != nil {
		return nil, err
	}
	file := held.DirectoryFile()
	if file == nil {
		return nil, os.ErrInvalid
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	entries, err := directoryKinds(file)
	if err != nil {
		return nil, err
	}
	return &observationDirectory{entries: entries, resolved: dir, stamp: directoryStampOf(info), privateFilter: repochange.NewPrivateDirectoryFilter(filepath.Join(root.Name(), filepath.FromSlash(dir)))}, nil
}

func acquireObservationRoot(ctx context.Context, store *indexStore) (*os.Root, func(), error) {
	root, release, err := store.navigation.Acquire(ctx, store.root.Path)
	if err != nil {
		return nil, nil, err
	}
	current, err := os.Stat(store.root.Path)
	if err == nil {
		var held os.FileInfo
		held, err = root.Stat(".")
		if err == nil && !os.SameFile(current, held) {
			err = errObservationChanged
		}
	}
	if err == nil && store.root.DirectoryIdentity != "" {
		err = checkAnchoredDirectory(store.root, root)
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return root, release, nil
}

func checkAnchoredDirectory(expected Root, root *os.Root) error {
	if fspath.CanonicalPath(expected.Path) != expected.Path {
		return os.ErrNotExist
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	identity, err := fspath.FileIdentity(file)
	if err == nil && identity != expected.DirectoryIdentity {
		return os.ErrNotExist
	}
	return err
}
