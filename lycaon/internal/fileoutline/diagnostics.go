package fileoutline

import (
	"errors"

	"github.com/lycaon/lycaon/internal/repomap"
)

// DefinitionError distinguishes unavailable analysis from an empty outline.
func (r Result) DefinitionError() error {
	if r.ParseFailure != nil {
		if r.ParseFailure.Incomplete() {
			return errors.Join(repomap.ErrDefinitionIncomplete, r.ParseFailure)
		}
		return errors.Join(repomap.ErrDefinitionUnavailable, r.ParseFailure)
	}
	if r.Diagnostics == nil {
		return nil
	}
	if r.Diagnostics.SkipReasons.ParseIncomplete != 0 {
		return repomap.ErrDefinitionIncomplete
	}
	if r.Diagnostics.SkipReasons.ParseFailed != 0 {
		return repomap.ErrDefinitionUnavailable
	}
	return nil
}

func outlineDiagnostics(source string, skip repomap.SkipStats) *repomap.Diagnostics {
	if source == "tree_sitter" || source == "markdown" {
		return nil
	}
	return &repomap.Diagnostics{
		SkipReasons: skip,
	}
}
