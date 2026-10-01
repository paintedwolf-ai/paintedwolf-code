package scan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// SourceGeneration is the scanner-facing delta between two immutable source
// snapshots. Watcher paths only request reconciliation; they never populate
// this structure directly.
type SourceGeneration struct {
	Snapshot      sourcesnapshot.Snapshot
	PreviousID    string
	UpsertedPaths []string
	DeletedPaths  []string
}

// DiffSourceGenerations derives executable targets and deletions from
// manifest facts, reading only the buckets the two generations do not share.
// Changed content, mode, or membership is an upsert.
func DiffSourceGenerations(ctx context.Context, store *sourcesnapshot.Store, previous, current sourcesnapshot.Snapshot) (SourceGeneration, error) {
	if strings.TrimSpace(current.ID) == "" {
		return SourceGeneration{}, fmt.Errorf("current source snapshot required")
	}
	if store == nil {
		return SourceGeneration{}, fmt.Errorf("source snapshot store required")
	}
	out := SourceGeneration{Snapshot: current, PreviousID: previous.ID}
	err := store.Diff(ctx, previous.ID, current.ID, func(change sourcesnapshot.Change) error {
		if change.After != nil {
			out.UpsertedPaths = append(out.UpsertedPaths, change.After.Path)
		} else {
			out.DeletedPaths = append(out.DeletedPaths, change.Before.Path)
		}
		return nil
	})
	if err != nil {
		return SourceGeneration{}, err
	}
	sort.Strings(out.UpsertedPaths)
	sort.Strings(out.DeletedPaths)
	return out, nil
}

func FullTargetSelection(snapshot sourcesnapshot.Snapshot) TargetSelection {
	return TargetSelection{
		Kind: api.ScanTargetFull, CaptureQuality: string(snapshot.Quality),
		AdmissionMode: string(snapshot.AdmissionMode),
	}
}

type TargetSelection struct {
	BaseSnapshotID string
	Kind           api.ScanTargetKind
	Paths          []string
	DeletedPaths   []string
	CaptureQuality string
	AdmissionMode  string
}

func GenerationTargetSelection(generation SourceGeneration) TargetSelection {
	return TargetSelection{
		Kind: api.ScanTargetPaths, Paths: append([]string(nil), generation.UpsertedPaths...),
		BaseSnapshotID: generation.PreviousID,
		DeletedPaths:   append([]string(nil), generation.DeletedPaths...),
		CaptureQuality: string(generation.Snapshot.Quality),
		AdmissionMode:  string(generation.Snapshot.AdmissionMode),
	}
}

// ValidateSnapshotTargets checks every target names an admitted file or a
// directory holding one, against the manifest rather than the filesystem.
func ValidateSnapshotTargets(ctx context.Context, store *sourcesnapshot.Store, snapshot sourcesnapshot.Snapshot, paths []string) ([]string, error) {
	paths = NormalizeScanPaths(paths)
	if len(paths) == 0 {
		return nil, nil
	}
	if store == nil {
		return nil, fmt.Errorf("source snapshot store required")
	}
	out := make([]string, 0, len(paths))
	for _, target := range paths {
		held := false
		for _, root := range snapshot.Roots {
			ok, err := store.HasPath(ctx, snapshot.ID, root.Path, target)
			if err != nil {
				return nil, err
			}
			if ok {
				held = true
				break
			}
		}
		if !held {
			return nil, fmt.Errorf("scan target %q is not present in source snapshot %s", target, snapshot.ID)
		}
		out = append(out, target)
	}
	return out, nil
}
