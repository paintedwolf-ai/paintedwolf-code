package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

type indexReaderFloor struct {
	sessionScratch string
	sessionOutput  string
	ready          bool
	env            string
	compiled       confine.AgentReadFloor
}

func newIndexReaderFloor(ctx context.Context) indexReaderFloor {
	scratch, output := sandbox.SessionDirsFromContext(ctx)
	return indexReaderFloor{
		sessionScratch: scratch,
		sessionOutput:  output,
	}
}

func (r *IndexReader) refreshAgentFloor() confine.AgentReadFloor {
	if r == nil || r.store == nil {
		return confine.AgentReadFloor{}
	}
	env := os.Getenv("LYCAON_SANDBOX_DENY_READ")
	if !r.floor.ready || r.floor.env != env {
		r.floor.compiled = confine.CompileAgentReadFloor(r.store.root.watchRoot(), r.floor.sessionScratch, r.floor.sessionOutput)
		r.floor.env = env
		r.floor.ready = true
	}
	return r.floor.compiled
}

func (r *IndexReader) isRootDeniedForScope(scope FileScope) bool {
	if r.store == nil {
		return false
	}
	if repochange.IsPrivatePathForRoot(r.store.root.watchRoot(), r.store.root.Path) {
		return true
	}
	if scope.Audience != HumanAudience && r.refreshAgentFloor().WhollyDenied() {
		return true
	}
	return false
}

func (r *IndexReader) isDeniedForScope(scope FileScope, rel string) bool {
	if r.store == nil {
		return false
	}
	if scope.Audience != HumanAudience {
		if r.refreshAgentFloor().IsDeniedRel(rel) {
			return true
		}
	}
	abs := filepath.Join(r.store.root.Path, filepath.FromSlash(rel))
	return repochange.IsPrivatePathForRoot(r.store.root.watchRoot(), abs)
}

func (r *IndexReader) collapsedDeniedPrefixes(scope FileScope) []string {
	if scope.Audience == HumanAudience {
		return nil
	}
	raw := r.refreshAgentFloor().DeniedRelPrefixes()
	if len(raw) == 0 {
		return nil
	}
	sorted := append([]string(nil), raw...)
	sort.Strings(sorted)
	collapsed := make([]string, 0, len(sorted))
	for _, p := range sorted {
		if p == "" || p == "." {
			continue
		}
		if len(collapsed) > 0 {
			prev := collapsed[len(collapsed)-1]
			if p == prev || strings.HasPrefix(p, prev+"/") {
				continue
			}
		}
		collapsed = append(collapsed, p)
	}
	return collapsed
}

func (r *IndexReader) subtractDeniedFileCount(ctx context.Context, scope FileScope, count int) (int, error) {
	for _, prefix := range r.collapsedDeniedPrefixes(scope) {
		var denied int
		// #nosec G202 -- scope.clause() selects fixed SQL predicates; prefix bounds are parameters.
		if err := r.tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM nodes INDEXED BY folded_path WHERE "+scope.clause()+" AND (lower(path)=? OR (lower(path)>=? AND lower(path)<?))",
			prefix, prefix+"/", prefix+"0").Scan(&denied); err != nil {
			return 0, err
		}
		r.RowsRead++
		count -= denied
	}
	if count < 0 {
		count = 0
	}
	return count, nil
}
