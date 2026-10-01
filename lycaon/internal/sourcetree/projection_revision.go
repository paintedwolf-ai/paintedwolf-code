package sourcetree

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// revision composes catalog summaries through disclosure exceptions, so its
// work scales with those exceptions rather than with the tree.
func (p *Projection) revision(ctx context.Context) (string, error) {
	physical := *p
	physical.Review = nil
	fingerprint, err := physical.directoryFingerprint(ctx, ".")
	return hex.EncodeToString(fingerprint[:]), err
}

func (p *Projection) directoryFingerprint(ctx context.Context, dir string) (pagedview.Fingerprint, error) {
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
	if !open {
		return result, nil
	}
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	index, err := p.Navigation.Children(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	body, err := index.Fingerprint(ctx, !rule.Recursive)

	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	body, err = p.disclosedChildrenFingerprint(ctx, dir, index, rule, body)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	state, err := p.Navigation.State(ctx, dir)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	body, err = sourcecatalog.DirectoryStateFingerprint(ctx, index, dir, state, body)
	return result.Combine(body), err
}

func (p *Projection) disclosedChildrenFingerprint(ctx context.Context, dir string, index *pagedview.RangeIndex[sourcecatalog.TreeItem], rule Disclosure, body pagedview.Fingerprint) (pagedview.Fingerprint, error) {
	for _, child := range p.Rules.Branches(Address{Root: p.Root, Path: dir}) {
		entry, err := p.Navigation.Entry(ctx, child)
		if errors.Is(err, pagedview.ErrMissing) {
			continue
		}
		if err != nil {
			return pagedview.Fingerprint{}, err
		}
		if !entry.IsDir {
			continue
		}
		item, _, err := index.Locate(ctx, sourcecatalog.DirectoryOrder(entry.Name, true))
		if err != nil {
			return pagedview.Fingerprint{}, err
		}
		original := item.BaselineFingerprint
		if rule.Recursive {
			original = item.Fingerprint
		}
		replacement, err := p.directoryFingerprint(ctx, child)
		if err != nil {
			return pagedview.Fingerprint{}, err
		}
		body = body.Combine(original).Combine(replacement)
	}
	return body, nil
}
