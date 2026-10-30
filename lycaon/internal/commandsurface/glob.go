package commandsurface

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// Glob expansion bounds. MaxGlobMatches caps one pattern; MaxGlobVisits caps
// the directory entries one plan examines.
const (
	MaxGlobMatches = 1000
	MaxGlobVisits  = 50000
)

// Glob budget limits name the bound a pattern exceeded.
const (
	GlobLimitMatches = "matches"
	GlobLimitEntries = "entries"
)

// ErrGlobBudget matches every GlobBudgetError.
var ErrGlobBudget = errors.New("glob expansion exceeded its budget")

// GlobBudgetError names the pattern that exceeded a bound and the bound.
type GlobBudgetError struct {
	Pattern string
	Limit   string
	Max     int
}

func (e *GlobBudgetError) Error() string {
	return fmt.Sprintf("%s: %q exceeds %d %s", ErrGlobBudget, e.Pattern, e.Max, e.Limit)
}

// Is lets errors.Is match ErrGlobBudget.
func (e *GlobBudgetError) Is(target error) bool { return target == ErrGlobBudget }

// GlobEntry is one directory entry a walk examines. Dir reports an entry that
// may prefix a later component; Link marks a symbolic link, which globstar
// never descends.
type GlobEntry struct {
	Name string
	Dir  bool
	Link bool
}

// GlobFiles is the filesystem view of one walk. Paths keep the pattern's own
// spelling: "" is the working directory and components join with "/". A
// missing or unreadable directory lists nothing; errors are reserved for
// cancellation and resource refusals.
type GlobFiles interface {
	// ListDir returns at most maximum+1 entries, so a walk can refuse past its
	// visit budget without listing the remainder.
	ListDir(ctx context.Context, dir string, maximum int) ([]GlobEntry, error)
	// Entry reports a component the pattern names literally.
	Entry(ctx context.Context, path string) (GlobEntry, bool, error)
	// Admit reports whether a candidate is visible to the operation; a match it
	// could not read is not a match.
	Admit(ctx context.Context, path string) (bool, error)
}

// GlobBudget is the capacity a walk may still consume. MatchGlob deducts the
// entries it listed and the matches it returned.
type GlobBudget struct {
	Matches int
	Visits  int
}

// MatchGlob returns the sorted matches of one pattern, as a shell with globstar
// and without dotglob does: hidden names match only a component that starts
// with a dot, and ** matches zero or more nonhidden directories. A pattern with
// no match returns none. Exceeding the budget returns a GlobBudgetError whose
// Max is the remaining capacity the walk was given.
func MatchGlob(ctx context.Context, files GlobFiles, pattern string, budget *GlobBudget) ([]string, error) {
	g := globber{ctx: ctx, files: files, maxMatches: budget.Matches, visitsLeft: budget.Visits}
	matches, err := g.expand(pattern)
	if errors.Is(err, errVisitBudget) {
		return nil, &GlobBudgetError{Pattern: pattern, Limit: GlobLimitEntries, Max: budget.Visits}
	}
	if err != nil {
		return nil, err
	}
	if len(matches) > g.maxMatches {
		return nil, &GlobBudgetError{Pattern: pattern, Limit: GlobLimitMatches, Max: budget.Matches}
	}
	budget.Visits = g.visitsLeft
	budget.Matches -= len(matches)
	return matches, nil
}

// GlobActive reports an unescaped `*`, `?`, or `[` anywhere in a pattern.
func GlobActive(pattern string) bool { return hasActiveGlob(pattern) }

// GlobScope is the filesystem view one plan's patterns expand against.
type GlobScope struct {
	// Dir is the absolute process directory relative patterns resolve under.
	Dir string
	// Readable reports whether the launched process may read an absolute path;
	// a match it could not read is not a match.
	Readable func(abs string) bool
	// MaxMatches and MaxVisits default to MaxGlobMatches and MaxGlobVisits.
	MaxMatches int
	MaxVisits  int
}

// ExpandGlobs replaces each unquoted glob argument with its sorted matches.
// A pattern with no match stays literal; an argument starting with `-` never
// expands. The result carries no remaining patterns, so it runs as rendered,
// and keeps each surviving argument's address mark.
func ExpandGlobs(ctx context.Context, stages []exec.Stage, scope GlobScope) ([]exec.Stage, bool, error) {
	maxMatches, maxVisits := scope.MaxMatches, scope.MaxVisits
	if maxMatches <= 0 {
		maxMatches = MaxGlobMatches
	}
	if maxVisits <= 0 {
		maxVisits = MaxGlobVisits
	}
	files := OSGlobFiles{Dir: scope.Dir, Readable: scope.Readable}
	visitsLeft := maxVisits
	out := make([]exec.Stage, len(stages))
	changed := false
	for i, stage := range stages {
		out[i] = stage
		if len(stage.Globs) == 0 {
			continue
		}
		args := make([]string, 0, len(stage.Args))
		addressed := make([]bool, 0, len(stage.Args))
		keep := func(j int, arg string) {
			args = append(args, arg)
			addressed = append(addressed, j < len(stage.Addressed) && stage.Addressed[j])
		}
		for j, arg := range stage.Args {
			pattern := stage.Globs[j]
			if pattern == "" || strings.HasPrefix(arg, "-") {
				keep(j, arg)
				continue
			}
			budget := GlobBudget{Matches: maxMatches, Visits: visitsLeft}
			matches, err := MatchGlob(ctx, files, pattern, &budget)
			var exceeded *GlobBudgetError
			if errors.As(err, &exceeded) {
				exceeded.Pattern = arg
				if exceeded.Limit == GlobLimitEntries {
					exceeded.Max = maxVisits
				}
				return nil, false, exceeded
			}
			if err != nil {
				return nil, false, err
			}
			visitsLeft = budget.Visits
			if len(matches) == 0 {
				keep(j, arg)
				continue
			}
			changed = true
			args = append(args, matches...)
			addressed = append(addressed, make([]bool, len(matches))...)
		}
		out[i].Args = args
		out[i].Globs = nil
		out[i].Addressed = nil
		if slices.Contains(addressed, true) {
			out[i].Addressed = addressed
		}
	}
	return out, changed, nil
}

var errVisitBudget = errors.New("glob visit budget exhausted")

type globber struct {
	ctx        context.Context
	files      GlobFiles
	maxMatches int
	visitsLeft int
}

// expand matches one pattern component by component.
func (g *globber) expand(pattern string) ([]string, error) {
	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	candidates := []string{""}
	if strings.HasPrefix(pattern, "/") {
		candidates = []string{"/"}
		pattern = strings.TrimLeft(pattern, "/")
	}
	components := strings.Split(pattern, "/")
	for k, component := range components {
		last := k == len(components)-1
		var next []string
		for _, base := range candidates {
			found, err := g.component(base, component, last && !dirOnly)
			if err != nil {
				return nil, err
			}
			next = append(next, found...)
			if len(next) > g.maxMatches && last {
				return next, nil
			}
		}
		candidates = next
		if len(candidates) == 0 {
			return nil, nil
		}
	}
	if dirOnly {
		for i := range candidates {
			candidates[i] += "/"
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

// component returns base's children matching one pattern component. Only the
// last component may match a non-directory.
func (g *globber) component(base, component string, last bool) ([]string, error) {
	switch {
	case component == "**":
		return g.globstar(base, last)
	case !hasActiveGlob(component):
		candidate := joinGlob(base, unescapeGlob(component))
		entry, found, err := g.files.Entry(g.ctx, candidate)
		if err != nil || !found || (!last && !entry.Dir) {
			return nil, err
		}
		return g.admitted(nil, candidate)
	}
	component = bashBracketNegation(component)
	entries, err := g.readDir(base)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(component, ".") && !strings.HasPrefix(component, `\.`) {
			continue
		}
		if ok, err := path.Match(component, name); err != nil || !ok {
			continue
		}
		if !last && !entry.Dir {
			continue
		}
		if out, err = g.admitted(out, joinGlob(base, name)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (g *globber) admitted(out []string, candidate string) ([]string, error) {
	ok, err := g.files.Admit(g.ctx, candidate)
	if err != nil || !ok {
		return out, err
	}
	return append(out, candidate), nil
}

// globstar matches base and every nonhidden directory under it; as the last
// component it matches every nonhidden entry under base instead. Symbolic
// links are never descended.
func (g *globber) globstar(base string, last bool) ([]string, error) {
	var out []string
	if !last {
		out = append(out, base)
	}
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := g.readDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, ".") {
				continue
			}
			candidate := joinGlob(dir, entry.Name)
			ok, err := g.files.Admit(g.ctx, candidate)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			descend := entry.Dir && !entry.Link
			if last || descend {
				out = append(out, candidate)
			}
			if last && len(out) > g.maxMatches {
				return nil
			}
			if descend {
				if err := walk(candidate); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(base); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *globber) readDir(dir string) ([]GlobEntry, error) {
	if err := g.ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := g.files.ListDir(g.ctx, dir, max(g.visitsLeft, 0))
	if err != nil {
		return nil, err
	}
	g.visitsLeft -= len(entries)
	if g.visitsLeft < 0 {
		return nil, errVisitBudget
	}
	return entries, nil
}

func joinGlob(base, name string) string {
	switch base {
	case "":
		return name
	case "/":
		return "/" + name
	default:
		return base + "/" + name
	}
}

// hasActiveGlob reports an unescaped `*`, `?`, or `[` in a pattern.
func hasActiveGlob(component string) bool {
	for i := 0; i < len(component); i++ {
		switch component[i] {
		case '\\':
			i++
		case '*', '?', '[':
			return true
		}
	}
	return false
}

func unescapeGlob(component string) string {
	var b strings.Builder
	for i := 0; i < len(component); i++ {
		if component[i] == '\\' && i+1 < len(component) {
			i++
		}
		b.WriteByte(component[i])
	}
	return b.String()
}

// bashBracketNegation rewrites the shell's `[!...]` to the `[^...]` path.Match reads.
func bashBracketNegation(component string) string {
	var b strings.Builder
	for i := 0; i < len(component); i++ {
		c := component[i]
		b.WriteByte(c)
		switch c {
		case '\\':
			if i+1 < len(component) {
				i++
				b.WriteByte(component[i])
			}
		case '[':
			if i+1 < len(component) && component[i+1] == '!' {
				b.WriteByte('^')
				i++
			}
		}
	}
	return b.String()
}
