package sourcecatalog

import (
	"context"
	"encoding/binary"

	"github.com/lycaon/lycaon/internal/pagedview"
)

func TreeRowFingerprint(rel, kind string, symlink, expanded bool, failure string) pagedview.Fingerprint {
	value := make([]byte, 0, len(rel)+len(kind)+len(failure)+31)
	for _, field := range []string{rel, kind, failure} {
		value = binary.AppendUvarint(value, uint64(len(field)))
		value = append(value, field...)
	}
	var flags byte
	if symlink {
		flags |= 1
	}
	if expanded {
		flags |= 2
	}
	value = append(value, flags)
	return pagedview.FingerprintOf(value)
}

func DirectoryBodyFingerprint(ctx context.Context, index *pagedview.RangeIndex[TreeItem], dir string, state DirectoryState, recursive bool) (pagedview.Fingerprint, error) {
	fingerprint, err := index.Fingerprint(ctx, !recursive)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	return DirectoryStateFingerprint(ctx, index, dir, state, fingerprint)
}

// The directory path keeps sibling coverage terms distinct under XOR composition.
func DirectoryStateFingerprint(ctx context.Context, index *pagedview.RangeIndex[TreeItem], dir string, state DirectoryState, fingerprint pagedview.Fingerprint) (pagedview.Fingerprint, error) {
	// Review may classify an absent path only after its parent listing is complete.
	fingerprint = fingerprint.Combine(TreeRowFingerprint(dir, "directory-coverage", false, state.Complete, ""))
	count, err := index.Count(ctx)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	if kind := DirectoryAncillary(state, count); kind != "" {
		fingerprint = fingerprint.Combine(TreeRowFingerprint(dir, kind, false, false, state.Failure))
	}
	return fingerprint, nil
}
