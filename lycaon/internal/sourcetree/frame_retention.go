package sourcetree

import (
	"context"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type FramePrefix struct {
	End         int64
	Fingerprint string
}

// Prefixes validate row positions as well as the rows themselves.
func (s *snapshot) prefix(ctx context.Context, end int64) (*FramePrefix, error) {
	if end <= 0 || end > s.total() {
		return nil, nil
	}
	for _, root := range s.roots {
		if root.projection.Review != nil {
			return nil, nil
		}
	}
	result := pagedview.FingerprintOf([]byte(s.revision.Intent + ":" + strconv.FormatInt(end, 10)))
	remaining := end
	for i := range s.roots {
		root := &s.roots[i]
		count := min(remaining, root.rows)
		fingerprint, err := root.projection.prefixFingerprint(ctx, ".", count)
		if err != nil {
			return nil, err
		}
		// Root order is independent of directory ordering.
		identity := root.root.ID + ":" + strconv.Itoa(i) + ":" + hex.EncodeToString(fingerprint[:])
		result = result.Combine(pagedview.FingerprintOf([]byte(identity)))
		remaining -= count
		if remaining == 0 {
			break
		}
	}
	return &FramePrefix{End: end, Fingerprint: hex.EncodeToString(result[:])}, nil
}

func (s *snapshot) retainFrame(ctx context.Context, frame *Frame, proofs []FramePrefix) error {
	for i := range s.roots {
		s.roots[i].projection.baseCache = pagedview.NewCache[string, projectedChildren](128, 2<<20)
	}
	var err error
	frame.Prefix, err = s.prefix(ctx, frame.Span.End)
	if err != nil {
		return err
	}
	proofs = slices.Clone(proofs)
	slices.SortFunc(proofs, func(a, b FramePrefix) int {
		if a.End > b.End {
			return -1
		}
		if a.End < b.End {
			return 1
		}
		return 0
	})
	for _, proof := range proofs {
		current, err := s.prefix(ctx, proof.End)
		if err != nil {
			return err
		}
		if current != nil && *current == proof {
			frame.RetainedPrefix = current
			break
		}
	}
	return nil
}

func (p *Projection) prefixFingerprint(ctx context.Context, dir string, count int64) (pagedview.Fingerprint, error) {
	open, err := p.open(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	symlink := false
	if dir != "." {
		entry, err := p.Navigation.Entry(ctx, dir)
		if err != nil {
			return pagedview.Fingerprint{}, err
		}
		symlink = entry.IsSymlink
	}
	result := sourcecatalog.TreeRowFingerprint(dir, "directory", symlink, open, "")
	if count <= 1 {
		return result, nil
	}
	count--
	marker, message, err := p.ancillary(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	if marker != "" {
		result = result.Combine(sourcecatalog.TreeRowFingerprint(dir, marker, false, false, message))
		count--
		if count == 0 {
			return result, nil
		}
	}
	last, within, err := p.baseChild(ctx, dir, count-1)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	before, err := p.childrenPrefix(ctx, dir, last)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	result = result.Combine(before)
	if !last.IsDir {
		return result.Combine(sourcecatalog.TreeRowFingerprint(last.Path, "file", last.IsSymlink, false, "")), nil
	}
	partial, err := p.prefixFingerprint(ctx, last.Path, within+1)
	return result.Combine(partial), err
}

func (p *Projection) childrenPrefix(ctx context.Context, dir string, last sourcecatalog.Entry) (pagedview.Fingerprint, error) {
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	index, err := p.Navigation.Children(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	key := sourcecatalog.DirectoryOrder(last.Name, last.IsDir)
	result, err := index.PrefixFingerprint(ctx, key, !rule.Recursive)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	_, replacements, err := p.baseChildren(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	for _, replacement := range replacements {
		previousKey := sourcecatalog.DirectoryOrder(replacement.entry.Name, true)
		if previousKey >= key {
			continue
		}
		item, _, err := index.Locate(ctx, previousKey)
		if err != nil {
			return pagedview.Fingerprint{}, err
		}
		original := item.BaselineFingerprint
		if rule.Recursive {
			original = item.Fingerprint
		}
		changed, err := p.directoryFingerprint(ctx, replacement.entry.Path)
		if err != nil {
			return pagedview.Fingerprint{}, err
		}
		result = result.Combine(original).Combine(changed)
	}
	return result, nil
}
