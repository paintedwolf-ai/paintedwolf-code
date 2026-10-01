package sourceledger

import (
	"context"
)

// fakeWorkingTree answers as a git working tree without running git.
type fakeWorkingTree struct {
	// treeOIDs maps rootAbs+"/"+path to the blob object id HEAD holds there.
	treeOIDs map[string]string
	fail     bool
}

func (f *fakeWorkingTree) TreeOIDs(_ context.Context, rootAbs string, paths []string) (map[string]string, bool) {
	if f.fail {
		return nil, false
	}
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		if oid, ok := f.treeOIDs[rootAbs+"/"+path]; ok {
			out[path] = oid
		}
	}
	return out, true
}

func commitLensFor(tree *fakeWorkingTree) CommitLens {
	return CommitLens{
		Git:       tree,
		Roots:     []LensRoot{{ID: "r1", Abs: "/tmp/source-ledger-test"}},
		Available: true,
	}
}
