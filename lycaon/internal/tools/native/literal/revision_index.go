package literal

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// revisionIndex is invocation-local membership, not another source owner.
// 1024 bytes per entry covers key/header capacity, spare load and overlapping
// old/new map tables; retained variable strings are charged separately. The
// geometric lease is acquired before growth and released once at invocation end.
type revisionIndex struct {
	entries        map[toolcontract.ExecutionRevision]struct{}
	used, capacity int64
	release        func()
}

func (r *revisionIndex) remember(ctx context.Context, memory *toolcontract.ProgramMemory, revision toolcontract.ExecutionRevision) (bool, error) {
	if _, seen := r.entries[revision]; seen {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	needed := r.used + 1024 + int64(len(revision.Role)+len(revision.Mode)+len(revision.Basis)+len(revision.RootID)+len(revision.Path)+len(revision.DocumentID)+len(revision.DiskSHA256)+len(revision.Representation)+len(revision.ContentSHA256))
	if needed > r.capacity {
		capacity := max(64<<10, max(needed, 2*r.capacity))
		release := func() {}
		if memory != nil {
			var err error
			release, err = memory.ReserveProgramState(ctx, capacity)
			if err != nil {
				return false, err
			}
		}
		if r.release != nil {
			r.release()
		}
		r.capacity, r.release = capacity, release
	}
	if r.entries == nil {
		r.entries = make(map[toolcontract.ExecutionRevision]struct{})
	}
	// Own the exact string backing charged above; a sliced path must not keep
	// a larger caller allocation alive behind a short map key.
	revision.Role = toolcontract.ExecutionRevisionRole(strings.Clone(string(revision.Role)))
	revision.Mode = toolcontract.ExecutionRevisionMode(strings.Clone(string(revision.Mode)))
	revision.Basis = toolcontract.ExecutionRevisionBasis(strings.Clone(string(revision.Basis)))
	revision.RootID = strings.Clone(revision.RootID)
	revision.Path = strings.Clone(revision.Path)
	revision.DocumentID = strings.Clone(revision.DocumentID)
	revision.DiskSHA256 = strings.Clone(revision.DiskSHA256)
	revision.Representation = toolcontract.SourceRepresentation(strings.Clone(string(revision.Representation)))
	revision.ContentSHA256 = strings.Clone(revision.ContentSHA256)
	r.entries[revision] = struct{}{}
	r.used = needed
	return false, nil
}
func (r *revisionIndex) close() {
	r.entries = nil
	if r.release != nil {
		r.release()
		r.release = nil
	}
	r.used, r.capacity = 0, 0
}
