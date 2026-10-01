package search

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

type pathGlobFilter struct {
	include       []sandbox.EntryGlob
	exclude       []sandbox.EntryGlob
	hiddenTargets []string
}

func compilePathGlobs(flags MatchFlags) (pathGlobFilter, error) {
	include, err := compilePathGlobList("include", flags.Include)
	if err != nil {
		return pathGlobFilter{}, err
	}
	exclude, err := compilePathGlobList("exclude", flags.Exclude)
	if err != nil {
		return pathGlobFilter{}, err
	}
	return pathGlobFilter{include: include, exclude: exclude}, nil
}

func compilePathGlobList(field string, patterns []string) ([]sandbox.EntryGlob, error) {
	var compiled []sandbox.EntryGlob
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		glob, err := sandbox.CompileEntryGlob(pattern)
		if err != nil {
			return nil, &MatchError{Message: fmt.Sprintf("invalid %s glob %q: %v", field, pattern, err)}
		}
		compiled = append(compiled, glob)
	}
	return compiled, nil
}

func (f pathGlobFilter) allows(rel string) bool {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	for _, glob := range f.exclude {
		if glob.Match(rel) {
			return false
		}
	}
	if len(f.include) == 0 {
		return true
	}
	for _, glob := range f.include {
		if glob.Match(rel) {
			return true
		}
	}
	return false
}
