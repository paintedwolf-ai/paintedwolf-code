package pongoplain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/flosch/pongo2/v6"
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
//
// With ObserveConditions set, each evaluated if or elif arm also records its
// atoms' values in Conditions. Partition, when set, names the group a render's
// scope belongs to, such as its recipient; vectors of different groups stay
// apart. Label, when set, names the case being rendered; each vector keeps the
// label of its first observation.
type BranchRecorder struct {
	Mu                sync.Mutex
	Aliases           map[string]string
	Hits              map[string]*BranchHit
	ObserveConditions bool
	Partition         func(scope map[string]any) string
	Label             func() string
	Conditions        map[string]*ConditionHit
	// Rendered counts executions of each named template, root or included.
	Rendered map[string]int
}

// templateNames names the templates Compile and NameTemplate saw, so a
// recorder can count their executions. Keys are weak, and an entry leaves the
// map when its template is collected, so naming never retains a template.
var templateNames sync.Map

// NameTemplate records the ref a composed set loaded tpl from.
func NameTemplate(tpl *pongo2.Template, name string) {
	if tpl == nil || name == "" {
		return
	}
	key := weak.Make(tpl)
	if _, loaded := templateNames.LoadOrStore(key, name); !loaded {
		runtime.AddCleanup(tpl, func(k weak.Pointer[pongo2.Template]) { templateNames.Delete(k) }, key)
	}
}

func templateName(tpl *pongo2.Template) (string, bool) {
	name, ok := templateNames.Load(weak.Make(tpl))
	if !ok {
		return "", false
	}
	return name.(string), true
}

func (r *BranchRecorder) recordRender(name string) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.Rendered == nil {
		r.Rendered = make(map[string]int)
	}
	if alias := r.Aliases[name]; alias != "" {
		name = alias
	}
	r.Rendered[name]++
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
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.Hits == nil {
		r.Hits = make(map[string]*BranchHit)
	}
	resolved := filename
	if alias := r.Aliases[filename]; alias != "" {
		resolved = alias
	}
	branchID := branchKey(resolved, line, col, kind)
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

func branchKey(template string, line, col int, kind BranchKind) string {
	return fmt.Sprintf("%s@L%d:C%d:%s", template, line, col, kind)
}
