package pagedview

import (
	"context"
	"crypto/sha256"
)

// Fingerprint summarizes uniquely addressed rows. Composition is independent of
// page splits; adapters include the ordering key in each row's fingerprint.
type Fingerprint [sha256.Size]byte

func FingerprintOf(value []byte) Fingerprint { return sha256.Sum256(value) }

func (f Fingerprint) Combine(other Fingerprint) Fingerprint {
	for i := range f {
		f[i] ^= other[i]
	}
	return f
}

func (x *RangeIndex[T]) Fingerprint(ctx context.Context, baseline bool) (Fingerprint, error) {
	if x.Root == 0 {
		return Fingerprint{}, nil
	}
	page, err := x.Store.Read(ctx, x.Root)
	if err != nil {
		return Fingerprint{}, err
	}
	branch, err := pageBranch(x.Root, page)
	if baseline {
		return branch.BaselineFingerprint, err
	}
	return branch.Fingerprint, err
}

// PrefixFingerprint excludes key and reads only its search path.
func (x *RangeIndex[T]) PrefixFingerprint(ctx context.Context, key string, baseline bool) (Fingerprint, error) {
	var result Fingerprint
	for id := x.Root; id != 0; {
		page, err := x.Store.Read(ctx, id)
		if err != nil {
			return Fingerprint{}, err
		}
		if len(page.Children) == 0 {
			for _, item := range page.Items {
				if item.Key >= key {
					break
				}
				fingerprint := item.Fingerprint
				if baseline {
					fingerprint = item.BaselineFingerprint
				}
				result = result.Combine(fingerprint)
			}
			break
		}
		at := childAt(page.Children, key)
		for _, child := range page.Children[:at] {
			fingerprint := child.Fingerprint
			if baseline {
				fingerprint = child.BaselineFingerprint
			}
			result = result.Combine(fingerprint)
		}
		id = page.Children[at].Page
	}
	return result, nil
}
