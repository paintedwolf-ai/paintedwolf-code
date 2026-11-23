package pongoplain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
)

// BranchKind classifies a conditional or loop branch in a template.
type BranchKind string

const (
	BranchIf    BranchKind = "if"
	BranchElif  BranchKind = "elif"
	BranchElse  BranchKind = "else"
	BranchFor   BranchKind = "for"
	BranchEmpty BranchKind = "empty"
)

// BranchSpec describes one statically discovered branch in a template source.
type BranchSpec struct {
	Index      int
	Line       int
	Col        int
	Kind       BranchKind
	Expression string
}

// BranchHit records runtime true/false evaluations for one branch site.
type BranchHit struct {
	Template    string
	RawTemplate string
	BranchID    string
	Line        int
	Col         int
	Kind        BranchKind
	TakenTrue   int
	TakenFalse  int
}

// BranchRecorder aggregates branch outcomes under Mu when stored on
// ActiveBranchRecorder. Tracked tags perform an atomic pointer load and skip
// recording when nil; concurrent renders attribute hits via each token's
// parsed filename (or Aliases entry) and must access Aliases and Hits under Mu.
type BranchRecorder struct {
	Mu      sync.Mutex
	Aliases map[string]string
	Hits    map[string]*BranchHit
}

// ActiveBranchRecorder is the opt-in global recorder consulted by tracked tags.
var ActiveBranchRecorder atomic.Pointer[BranchRecorder]

// InlineTemplateID returns the deterministic synthetic filename used when
// Compile parses an isolated template string.
func InlineTemplateID(source string) string {
	sum := sha256.Sum256([]byte(source))
	return "inline:" + hex.EncodeToString(sum[:8])
}

func (r *BranchRecorder) record(filename string, line, col int, kind BranchKind, taken bool) {
	if r == nil {
		return
	}
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.Hits == nil {
		r.Hits = make(map[string]*BranchHit)
	}
	resolved := filename
	if alias := r.Aliases[filename]; alias != "" {
		resolved = alias
	}
	branchID := fmt.Sprintf("%s@L%d:C%d:%s", resolved, line, col, kind)
	hit, ok := r.Hits[branchID]
	if !ok {
		hit = &BranchHit{
			Template:    resolved,
			RawTemplate: filename,
			BranchID:    branchID,
			Line:        line,
			Col:         col,
			Kind:        kind,
		}
		r.Hits[branchID] = hit
	}
	if taken {
		hit.TakenTrue++
	} else {
		hit.TakenFalse++
	}
}
