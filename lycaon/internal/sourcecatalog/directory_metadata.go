package sourcecatalog

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// One lookahead entry lets a short directory publish complete membership once.
type directoryBatch struct {
	file interface {
		ReadDir(int) ([]directoryEntry, error)
	}
	next directoryEntry
	done bool
}

func (r *directoryBatch) read(limit int) ([]directoryEntry, bool, error) {
	entries := make([]directoryEntry, 0, limit+1)
	if r.next != nil {
		entries = append(entries, r.next)
		r.next = nil
	}
	for !r.done && len(entries) <= limit {
		batch, err := r.file.ReadDir(limit + 1 - len(entries))
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			r.done = true
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	if len(entries) <= limit {
		return entries, r.done, nil
	}
	r.next = entries[limit]
	return entries[:limit], false, nil
}

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

type directoryEntry interface {
	Name() string
	IsDir() bool
	Type() os.FileMode
}

type directoryKind struct {
	name string
	kind os.FileMode
}

func (e directoryKind) Name() string      { return e.name }
func (e directoryKind) IsDir() bool       { return e.kind.IsDir() }
func (e directoryKind) Type() os.FileMode { return e.kind }

type directoryKindReader struct{ file *os.File }

// The descriptor fixes the directory; entries expose no path-based metadata lookup.
func (r *directoryKindReader) ReadDir(n int) ([]directoryEntry, error) {
	entries, err := r.file.ReadDir(n)
	kinds := make([]directoryEntry, len(entries))
	for i, entry := range entries {
		kinds[i] = directoryKind{name: entry.Name(), kind: entry.Type()}
	}
	return kinds, err
}

func DirectoryOrder(name string, isDir bool) string {
	kind := "1"
	if isDir {
		kind = "0"
	}
	return kind + strings.ToLower(name) + "\x00" + name
}

// directoryOrderKind recovers the kind a key was ordered by.
func directoryOrderKind(key string) (isDir bool) { return strings.HasPrefix(key, "0") }

func directoryUnresolved(ctx context.Context, children *pagedview.RangeIndex[TreeItem], state DirectoryState) (int64, error) {
	if state.Failure != "" {
		return 0, nil
	}
	pending, err := children.Unresolved(ctx)
	if !state.Complete && state.Failure == "" {
		pending++
	}
	return pending, err
}

// DirectoryStamp is a directory inode's own modification and change times in
// nanoseconds. Adding, removing, or renaming an entry moves both, so a listing
// recorded with a stamp still describes the directory while the stamp matches.
type DirectoryStamp struct {
	Modified int64
	Changed  int64
}

func (s DirectoryStamp) known() bool { return s != DirectoryStamp{} }
