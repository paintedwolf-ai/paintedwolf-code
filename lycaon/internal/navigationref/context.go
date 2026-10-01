package navigationref

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

type contextIndex map[string][]api.NavigationTarget

func indexContext(context *api.SourceContext) contextIndex {
	index := contextIndex{}
	if context == nil || context.Truncated {
		return index
	}
	for _, target := range sourceref.Merge(context).Locations {
		name := path.Base(target.Path)
		index[name] = append(index[name], target)
	}
	return index
}

// bind resolves abbreviations only within the supplied source context.
func (index contextIndex) bind(ref api.NavigationReference) (api.NavigationReference, bool) {
	qualified := strings.HasPrefix(ref.Mention, "@") || strings.Contains(ref.Mention, "://") || filepath.IsAbs(ref.Mention) || strings.HasPrefix(ref.Mention, "./")
	var matches []api.NavigationTarget
	for _, target := range index[path.Base(ref.Path)] {
		if target.ProjectID != ref.ProjectID || (qualified && (target.RootID != ref.RootID || target.WorkerID != ref.WorkerID)) {
			continue
		}
		if target.Path == ref.Path || (!qualified && strings.HasSuffix(target.Path, "/"+ref.Path)) {
			matches = append(matches, target)
		}
	}
	if len(matches) == 0 {
		return ref, false
	}
	if len(matches) > 32 {
		ref.Status, ref.RootID, ref.WorkerID = api.NavigationUnavailable, "", ""
		return ref, true
	}
	if len(matches) > 1 {
		ref.Status = api.NavigationAmbiguous
		ref.RootID, ref.WorkerID = "", ""
		ref.Candidates = matches
		return ref, true
	}
	target := matches[0]
	ref.RootID, ref.Path, ref.WorkerID, ref.EntryKind = target.RootID, target.Path, target.WorkerID, target.EntryKind
	ref.Status = api.NavigationResolved
	return ref, true
}
