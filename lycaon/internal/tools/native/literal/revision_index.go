package literal

import (
	"context"
	"hash/fnv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const revisionPageEntries = 64

// revisionIndex keeps exact membership in fixed-capacity pages.
type revisionIndex struct {
	buckets []*revisionPage
	release func()
}
type revisionPage struct {
	entries  map[toolcontract.ExecutionRevision]struct{}
	next     *revisionPage
	releases []func()
}

func (r *revisionIndex) remember(ctx context.Context, memory *toolcontract.ProgramMemory, revision toolcontract.ExecutionRevision) (bool, error) {
	ctx = toolcontract.WithMemoryPhase(ctx, toolcontract.InputProvenance)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(r.buckets) == 0 {
		if memory != nil {
			release, err := memory.ReserveProgramState(ctx, 4096)
			if err != nil {
				return false, err
			}
			r.release = release
		}
		r.buckets = make([]*revisionPage, 256)
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(revision.RootID))
	_, _ = hash.Write([]byte(revision.Path))
	index := hash.Sum64() % uint64(len(r.buckets))
	page := r.buckets[index]
	for current := page; current != nil; current = current.next {
		if _, ok := current.entries[revision]; ok {
			return true, nil
		}
	}
	if page == nil || len(page.entries) == revisionPageEntries {
		release := func() {}
		if memory != nil {
			var err error
			release, err = memory.ReserveProgramState(ctx, revisionPageEntries*512)
			if err != nil {
				return false, err
			}
		}
		page = &revisionPage{entries: make(map[toolcontract.ExecutionRevision]struct{}, revisionPageEntries), next: page, releases: []func(){release}}
		r.buckets[index] = page
	}
	size := int64(len(revision.Role) + len(revision.Mode) + len(revision.Basis) + len(revision.RootID) + len(revision.Path) + len(revision.DocumentID) + len(revision.DiskSHA256) + len(revision.Representation) + len(revision.ContentSHA256))
	if memory != nil {
		release, err := memory.ReserveProgramState(ctx, size+128)
		if err != nil {
			return false, err
		}
		page.releases = append(page.releases, release)
	}
	revision.Role = toolcontract.ExecutionRevisionRole(strings.Clone(string(revision.Role)))
	revision.Mode = toolcontract.ExecutionRevisionMode(strings.Clone(string(revision.Mode)))
	revision.Basis = toolcontract.ExecutionRevisionBasis(strings.Clone(string(revision.Basis)))
	revision.RootID = strings.Clone(revision.RootID)
	revision.Path = strings.Clone(revision.Path)
	revision.DocumentID = strings.Clone(revision.DocumentID)
	revision.DiskSHA256 = strings.Clone(revision.DiskSHA256)
	revision.Representation = toolcontract.SourceRepresentation(strings.Clone(string(revision.Representation)))
	revision.ContentSHA256 = strings.Clone(revision.ContentSHA256)
	page.entries[revision] = struct{}{}
	return false, nil
}
func (r *revisionIndex) close() {
	for _, page := range r.buckets {
		for page != nil {
			for _, release := range page.releases {
				release()
			}
			page = page.next
		}
	}
	r.buckets = nil
	if r.release != nil {
		r.release()
		r.release = nil
	}
}
