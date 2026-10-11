package sourceapi

import (
	"context"

	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// commitTrees holds one commit's blob ids for the files a batch names. Git
// reads a tree as cheaply for many paths as for one, so a batch resolves each
// folder once rather than once per file.
type commitTrees struct {
	byRoot map[string]commitTree
}

type commitTree struct {
	head string
	oids map[string]string
}

// oid answers the blob a file had at the batch's commit. The second result is
// false when this batch did not resolve that folder, or resolved a different
// commit, and the caller reads the tree itself.
func (t *commitTrees) oid(rootID, head, path string) (string, bool) {
	if t == nil || head == "" {
		return "", false
	}
	tree, ok := t.byRoot[rootID]
	if !ok || tree.head != head {
		return "", false
	}
	return tree.oids[path], true
}

// resolveCommitTrees reads one tree per folder the batch names. A folder Git
// cannot answer for is left out, so its files fall back to a single read.
func (s *Comparisons) resolveCommitTrees(ctx context.Context, p *project.Project, sources []wire.SourceComparisonSelector) *commitTrees {
	paths := map[string][]string{}
	for _, source := range sources {
		if source.Commit == nil || source.Commit.RootID == "" || source.Commit.Path == "" {
			continue
		}
		paths[source.Commit.RootID] = append(paths[source.Commit.RootID], source.Commit.Path)
	}
	mgr := s.Git.Manager()
	if len(paths) == 0 {
		return nil
	}
	trees := &commitTrees{byRoot: make(map[string]commitTree, len(paths))}
	for rootID, files := range paths {
		rootAbs, _, mapped := s.resolveRootRepoPosition(ctx, p, rootID)
		if !mapped {
			continue
		}
		head, err := commitComparisonHead(ctx, mgr, rootAbs)
		if err != nil || head == "" {
			continue
		}
		oids, err := mgr.TreeOIDs(ctx, rootAbs, head, files)
		if err != nil {
			continue
		}
		trees.byRoot[rootID] = commitTree{head: head, oids: oids}
	}
	if len(trees.byRoot) == 0 {
		return nil
	}
	return trees
}
