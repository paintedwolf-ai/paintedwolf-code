package sourceobservation

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
)

func normalizeFileDefaults(f File) File {
	if f.Provenance == "" {
		f.Provenance = observation.ProvenanceServerFSRequest
	}
	if f.Completeness == "" {
		f.Completeness = observation.CompletenessComplete
	}
	if f.Layer == "" {
		f.Layer = layeredfs.LayerTrunk
	}
	if f.Uncertain {
		f.Provenance = observation.ProvenanceUncertainOverflow
		f.Completeness = observation.CompletenessPartialKernelCached
	}
	return f
}

// FileFromSourceObservation converts a structured layeredfs observation into a Store File row.
func FileFromSourceObservation(obs observation.SourceObservation, gitOID string, mode int64) File {
	return normalizeFileDefaults(File{
		Path:             obs.Path.Rel,
		SHA256:           obs.SHA256,
		GitOID:           gitOID,
		Size:             obs.Size,
		Mode:             mode,
		ModifiedNS:       obs.ModTimeNS,
		Provenance:       obs.Provenance,
		Completeness:     obs.Completeness,
		Layer:            obs.Layer,
		Uncertain:        obs.Uncertain,
		SourceGeneration: obs.SourceGeneration,
		Ino:              obs.Ino,
	})
}

// ToSourceObservation converts a stored File row back into a structured observation.SourceObservation.
func (f File) ToSourceObservation(viewID, rootID string) observation.SourceObservation {
	norm := normalizeFileDefaults(f)
	return observation.SourceObservation{
		ObservationID:    fmt.Sprintf("store-%s-%s-%d", rootID, norm.Path, norm.SourceGeneration),
		Path:             layeredfs.QualifiedPath{RootID: rootID, Rel: norm.Path},
		Layer:            norm.Layer,
		Kind:             layeredfs.ObserveRead,
		Provenance:       norm.Provenance,
		Completeness:     norm.Completeness,
		Uncertain:        norm.Uncertain,
		SHA256:           norm.SHA256,
		Size:             norm.Size,
		ModTimeNS:        norm.ModifiedNS,
		Ino:              norm.Ino,
		SourceGeneration: norm.SourceGeneration,
	}
}

// RecordSourceObservation stores a structured observation.SourceObservation for rootPath.
// Private layer observations cannot be overwritten by trunk observations.
func (s *Store) RecordSourceObservation(
	ctx context.Context,
	rootPath string,
	obs observation.SourceObservation,
	gitOID string,
	mode int64,
) error {
	return s.Put(ctx, rootPath, []File{FileFromSourceObservation(obs, gitOID, mode)})
}

// RecordWatcherOverflow marks all trunk observations for rootPath as uncertain due to a
// watcher gap or overflow so missing events are never equated with unchanged content.
func (s *Store) RecordWatcherOverflow(ctx context.Context, rootPath string, reason string) error {
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, `
UPDATE observed_files
SET uncertain = 1,
    provenance = ?,
    completeness = ?,
    source_generation = source_generation + 1
WHERE root_path = ? AND layer != ?`,
		string(observation.ProvenanceUncertainOverflow),
		string(observation.CompletenessPartialKernelCached),
		rootPath,
		string(layeredfs.LayerPrivate),
	)
	return err
}

// SyncFromTracker synchronizes recorded observations from an observation.Tracker into the Store
// for rootPath and returns any pending trunk.changed notice without granting authority or
// selecting merge bases.
func (s *Store) SyncFromTracker(
	ctx context.Context,
	rootPath string,
	rootID string,
	tracker *observation.Tracker,
) (*observation.TrunkChangedNotice, error) {
	if tracker == nil {
		return nil, nil
	}
	obsList := tracker.Observations()
	files := make([]File, 0, len(obsList))
	hasOverflow := false
	for _, obs := range obsList {
		if rootID != "" && obs.Path.RootID != rootID {
			continue
		}
		if obs.Uncertain || obs.Provenance == observation.ProvenanceUncertainOverflow {
			hasOverflow = true
		}
		files = append(files, FileFromSourceObservation(obs, "", 0o644))
	}
	if len(files) > 0 {
		if err := s.Put(ctx, rootPath, files); err != nil {
			return nil, err
		}
	}
	if hasOverflow {
		if err := s.RecordWatcherOverflow(ctx, rootPath, "tracker_overflow"); err != nil {
			return nil, err
		}
	}
	notice, ok := tracker.PendingTrunkChangedNotice()
	if !ok {
		return nil, nil
	}
	return notice, nil
}

// MatchesObservedIdentity checks whether current matches recorded without ever relying
// on timestamp-only (ModifiedNS) equality as proof of unchanged content.
func MatchesObservedIdentity(recorded, current File) bool {
	if recorded.Uncertain || current.Uncertain {
		return false
	}
	if recorded.Provenance == observation.ProvenanceUncertainOverflow || current.Provenance == observation.ProvenanceUncertainOverflow {
		return false
	}
	if recorded.Size != current.Size {
		return false
	}
	if recorded.Ino != 0 && current.Ino != 0 && recorded.Ino != current.Ino {
		return false
	}
	if recorded.SHA256 == "" || current.SHA256 == "" {
		return false
	}
	return recorded.SHA256 == current.SHA256
}

// DeletePaths removes paths that left the tree.
func (s *Store) DeletePaths(ctx context.Context, rootPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, path := range paths {
		if _, err := tx.ExecContext(ctx, `DELETE FROM observed_files WHERE root_path = ? AND path = ?`, rootPath, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteRoot removes a detached root's rebuildable observations.
func (s *Store) DeleteRoot(ctx context.Context, rootPath string) error {
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, `DELETE FROM observed_files WHERE root_path = ?`, rootPath)
	return err
}

// Clear removes every rebuildable observation and compacts the cache file.
func (s *Store) Clear(ctx context.Context) error {
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM observed_files`); err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, `VACUUM`)
	return err
}

// Close releases the cache database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

