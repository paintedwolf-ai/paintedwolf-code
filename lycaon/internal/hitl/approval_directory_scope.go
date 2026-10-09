package hitl

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/confine"
)

// approvalDirectoryScopes preserves host candidate order before ladder sorting.
func approvalDirectoryScopes(options []ApprovalOption) []string {
	var paths []string
	for _, option := range options {
		if option.DirectoryScope != "" && !slices.Contains(paths, option.DirectoryScope) {
			paths = append(paths, option.DirectoryScope)
		}
	}
	return paths
}

// validateDirectoryScopes binds every presentation candidate to its exact offered authority.
func (p *ApprovalPlan) validateDirectoryScopes() error {
	for index, path := range p.DirectoryScopes {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || (index > 0 && !confine.PathStrictlyUnder(p.DirectoryScopes[index-1], path)) {
			return fmt.Errorf("directory scopes must be canonical ancestors")
		}
	}
	for _, option := range p.Options {
		if option.DirectoryScope == "" {
			continue
		}
		if !slices.Contains(p.DirectoryScopes, option.DirectoryScope) {
			return fmt.Errorf("option %q names an unoffered directory", option.ID)
		}
		bound := false
		for _, delta := range option.Authority {
			if delta.Kind == AuthorityGrantedPath && delta.GrantedPath != nil && delta.GrantedPath.Tree && !delta.GrantedPath.Write && delta.GrantedPath.Path == option.DirectoryScope {
				bound = true
			}
		}
		if !bound {
			return fmt.Errorf("option %q directory differs from its authority", option.ID)
		}
	}
	return nil
}
