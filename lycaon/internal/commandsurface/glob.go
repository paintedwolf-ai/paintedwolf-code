package commandsurface

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
	g := globber{ctx: ctx, scope: scope, maxMatches: scope.MaxMatches, visitsLeft: scope.MaxVisits}
	if g.maxMatches <= 0 {
		g.maxMatches = MaxGlobMatches
	}
	if g.visitsLeft <= 0 {
		g.visitsLeft = MaxGlobVisits
	}
	maxVisits := g.visitsLeft
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
			matches, err := g.expand(pattern)
			if errors.Is(err, errVisitBudget) {
				return nil, false, &GlobBudgetError{Pattern: arg, Limit: GlobLimitEntries, Max: maxVisits}
			}
			if err != nil {
				return nil, false, err
			}
			if len(matches) > g.maxMatches {
				return nil, false, &GlobBudgetError{Pattern: arg, Limit: GlobLimitMatches, Max: g.maxMatches}
			}
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
	scope      GlobScope
	maxMatches int
	visitsLeft int
}

// expand matches one pattern component by component, as a shell with
// globstar and without dotglob does.
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
		name := unescapeGlob(component)
		candidate := joinGlob(base, name)
		abs := g.abs(candidate)
		if !g.readable(abs) || !entryMatches(abs, last) {
			return nil, nil
		}
		return []string{candidate}, nil
	}
	component = bashBracketNegation(component)
	entries, err := g.readDir(base)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(component, ".") && !strings.HasPrefix(component, `\.`) {
			continue
		}
		if ok, err := path.Match(component, name); err != nil || !ok {
			continue
		}
		candidate := joinGlob(base, name)
		if !g.readable(g.abs(candidate)) {
			continue
		}
		if !last && !g.isDir(candidate, entry) {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

// globstar matches base and every non-hidden directory under it; as the last
// component it matches every non-hidden entry under base instead.
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
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			candidate := joinGlob(dir, name)
			if !g.readable(g.abs(candidate)) {
				continue
			}
			// Symlinked directories are not descended, as in a shell's globstar.
			isDir := entry.IsDir()
			if last || isDir {
				out = append(out, candidate)
			}
			if last && len(out) > g.maxMatches {
				return nil
			}
			if isDir {
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

func (g *globber) readDir(rel string) ([]os.DirEntry, error) {
	if err := g.ctx.Err(); err != nil {
		return nil, err
	}
	abs := g.abs(rel)
	if !g.readable(abs) {
		return nil, nil
	}
	entries, listed := listDir(abs)
	if !listed {
		return nil, nil
	}
	g.visitsLeft -= len(entries)
	if g.visitsLeft < 0 {
		return nil, errVisitBudget
	}
	return entries, nil
}

func (g *globber) isDir(rel string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(g.abs(rel))
	return err == nil && info.IsDir()
}

func (g *globber) abs(rel string) string {
	if strings.HasPrefix(rel, "/") {
		return filepath.FromSlash(rel)
	}
	if rel == "" {
		return g.scope.Dir
	}
	return filepath.Join(g.scope.Dir, filepath.FromSlash(rel))
}

func (g *globber) readable(abs string) bool {
	return g.scope.Readable == nil || g.scope.Readable(abs)
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

// hasActiveGlob reports an unescaped `*`, `?`, or `[` in a pattern component.
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

// entryMatches reports whether a literal component names an entry: a missing
// or unreadable entry matches nothing, as in a shell, and only the last
// component may be a non-directory.
func entryMatches(abs string, last bool) bool {
	info, err := os.Stat(abs)
	return err == nil && (last || info.IsDir())
}

// listDir reads a directory; one the process cannot list contributes no
// matches, as in a shell.
func listDir(abs string) ([]os.DirEntry, bool) {
	entries, err := os.ReadDir(abs)
	return entries, err == nil
}
