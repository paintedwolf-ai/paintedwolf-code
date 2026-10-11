package sourcescope

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// Exclusion reasons become boundary details.
const (
	ReasonFloor    = "floor"
	ReasonDeclared = "declared"
	ReasonIgnored  = "ignored"
)

// Scope applies one plane's admission policy and is safe for concurrent use.
type Scope struct {
	root       string
	plane      Plane
	floor      []gitignore.Pattern
	include    []gitignore.Pattern
	exclude    []gitignore.Pattern
	deferred   []gitignore.Pattern
	collapsed  []gitignore.Pattern
	boundaries []string
	identity   string

	mu   sync.Mutex
	own  *scopedstore.LRU[[]gitignore.Pattern]
	dirs map[string]dirDecision
}

type dirDecision struct {
	detail string
	prune  bool
}

type Options struct {
	Plane Plane
	// Floor patterns exclude paths at any depth and take precedence over project settings.
	Floor    []string
	Declared Declared
}

func New(root string, opts Options) *Scope {
	raw, err := json.Marshal(opts)
	if err != nil {
		panic(fmt.Errorf("encode source scope identity: %w", err))
	}
	digest := sha256.Sum256(raw)
	s := &Scope{
		root:     root,
		plane:    opts.Plane,
		identity: hex.EncodeToString(digest[:]),
		own:      scopedstore.New[[]gitignore.Pattern](scopeIgnoreDirectoryLimit),
		dirs:     make(map[string]dirDecision),
	}
	s.floor = patternsAtAnyDepth(opts.Floor)
	s.deferred = patternsAtAnyDepth(opts.Plane.DeferredDirectories)
	s.collapsed = patternsAtAnyDepth(opts.Plane.CollapsedDirectories)
	s.boundaries = append([]string(nil), opts.Plane.BoundaryDirectories...)
	for _, p := range opts.Declared.Include {
		s.include = append(s.include, gitignore.ParsePattern(p, nil))
	}
	for _, p := range opts.Declared.Exclude {
		s.exclude = append(s.exclude, gitignore.ParsePattern(p, nil))
	}
	return s
}

// Multi-segment patterns also match below the root.
func patternsAtAnyDepth(raw []string) []gitignore.Pattern {
	out := make([]gitignore.Pattern, 0, len(raw))
	for _, p := range raw {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") && !strings.HasPrefix(p, "**/") {
			p = "**/" + p
		}
		out = append(out, gitignore.ParsePattern(p, nil))
	}
	return out
}

// Identity covers admission options; ignore-file changes are tracked by the source watcher.
func (s *Scope) Identity() string { return s.identity }

func (s *Scope) Budgets() sandbox.SurveyBudgets { return s.plane.Budgets }

func (s *Scope) SurveyOptions(base sandbox.SurveyOptions) sandbox.SurveyOptions {
	base.Scope = s
	base.Budgets = s.plane.Budgets
	return base
}

// PruneDir assumes all ancestor directories were admitted.
func (s *Scope) PruneDir(rel, _ string) (string, bool) {
	rel = cleanRel(rel)
	if rel == "." {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decideDirLocked(rel)
}

// DeferDir assigns traversal priority without excluding paths.
func (s *Scope) DeferDir(rel, _ string) bool {
	return s.matchesOrIgnored(rel, s.deferred)
}

// CollapseDir reports whether a recursive expansion leaves rel closed.
func (s *Scope) CollapseDir(rel, _ string) bool {
	return s.matchesOrIgnored(rel, s.collapsed)
}

// Ignored directories count only when the plane reads ignore files for priority.
func (s *Scope) matchesOrIgnored(rel string, patterns []gitignore.Pattern) bool {
	rel = cleanRel(rel)
	if rel == "." {
		return false
	}
	segments := strings.Split(rel, "/")
	if matchAny(patterns, segments, true) == gitignore.Exclude {
		return true
	}
	if !s.plane.DeferIgnored {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ignoredLocked(segments, true)
}

// AdmitFile assumes the parent directory was admitted.
func (s *Scope) AdmitFile(rel, _ string) bool {
	rel = cleanRel(rel)
	if rel == "." {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, prune := s.decideLocked(rel, false)
	return !prune
}

// AdmitPath checks the entry and every ancestor for paths received outside a directory walk.
func (s *Scope) AdmitPath(rel string, isDir bool) bool {
	rel = cleanRel(rel)
	if rel == "." {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	segments := strings.Split(rel, "/")
	for i := 1; i < len(segments); i++ {
		if _, prune := s.decideDirLocked(strings.Join(segments[:i], "/")); prune {
			return false
		}
	}
	if isDir {
		_, prune := s.decideDirLocked(rel)
		return !prune
	}
	_, prune := s.decideLocked(rel, false)
	return !prune
}

func (s *Scope) decideDirLocked(rel string) (string, bool) {
	if d, ok := s.dirs[rel]; ok {
		return d.detail, d.prune
	}
	detail, prune := s.decideLocked(rel, true)
	s.dirs[rel] = dirDecision{detail: detail, prune: prune}
	return detail, prune
}

// The floor takes precedence over project declarations and ignore files.
func (s *Scope) decideLocked(rel string, isDir bool) (string, bool) {
	segments := strings.Split(rel, "/")
	if matchAny(s.floor, segments, isDir) == gitignore.Exclude {
		return ReasonFloor, true
	}
	if matchAny(s.exclude, segments, isDir) == gitignore.Exclude {
		return ReasonDeclared, true
	}
	if matchAny(s.include, segments, isDir) == gitignore.Exclude {
		// Explicit includes override ignore files.
		return "", false
	}
	if s.plane.IgnoreFiles && s.ignoredLocked(segments, isDir) {
		return ReasonIgnored, true
	}
	return "", false
}

func (s *Scope) ignoredLocked(segments []string, isDir bool) bool {
	stack := make([][]gitignore.Pattern, 0, len(segments))
	stack = append(stack, s.ownLocked("."))
	for i := 1; i < len(segments); i++ {
		stack = append(stack, s.ownLocked(strings.Join(segments[:i], "/")))
	}
	return lastMatch(stack, segments, isDir) == gitignore.Exclude
}

func (s *Scope) ownLocked(relDir string) []gitignore.Pattern {
	if patterns, ok := s.own.Load(relDir); ok {
		return patterns
	}
	patterns := directoryPatterns(s.root, relDir)
	s.own.Store(relDir, patterns)
	return patterns
}

// Any match counts, including negated patterns; the caller determines inclusion or exclusion.
func matchAny(patterns []gitignore.Pattern, segments []string, isDir bool) gitignore.MatchResult {
	for i := len(patterns) - 1; i >= 0; i-- {
		if patterns[i].Match(segments, isDir) != gitignore.NoMatch {
			return gitignore.Exclude
		}
	}
	return gitignore.NoMatch
}

func cleanRel(rel string) string {
	rel = strings.Trim(strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/")), "/")
	if rel == "" {
		return "."
	}
	return path.Clean(rel)
}
