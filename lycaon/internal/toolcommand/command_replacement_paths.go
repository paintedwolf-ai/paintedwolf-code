package toolcommand

import (
	"path"
	"path/filepath"
	"strings"
)

// replacementPathArgs identifies paths that require command-envelope rewriting.
var replacementPathArgs = map[string]struct {
	single      []string
	list        []string
	pairs       string
	projectOnly bool
}{
	"read":            {single: []string{"path"}},
	"list_dir":        {single: []string{"path"}},
	"jq":              {single: []string{"path"}},
	"grep":            {single: []string{"path"}},
	"find":            {single: []string{"path"}},
	"git_stash":       {projectOnly: true, list: []string{"paths"}},
	"git_branches":    {projectOnly: true},
	"git_ref":         {projectOnly: true},
	"git_checkout":    {projectOnly: true},
	"git_stash_list":  {projectOnly: true},
	"git_compare":     {projectOnly: true},
	"git_merge":       {projectOnly: true, list: []string{"paths"}},
	"git_log":         {projectOnly: true, single: []string{"path"}},
	"git_show":        {projectOnly: true, single: []string{"path"}},
	"git_blame":       {projectOnly: true, single: []string{"path"}},
	"extract_archive": {single: []string{"path", "dest"}},
	"stat":            {list: []string{"paths"}},
	"wc":              {list: []string{"paths"}},
	"delete":          {list: []string{"paths"}},
	"mkdir":           {list: []string{"paths"}},
	"chmod":           {list: []string{"paths"}},
	"chown":           {list: []string{"paths"}},
	"git_status":      {projectOnly: true, list: []string{"paths"}},
	"git_diff":        {projectOnly: true, list: []string{"paths"}},
	"git_restore":     {projectOnly: true, list: []string{"paths"}},
	"git_commit":      {projectOnly: true, list: []string{"paths"}},
	"copy":            {pairs: "copies"},
	"move":            {pairs: "moves"},
	"diff":            {single: []string{"path_a", "path_b"}},
	"http_request":    {single: []string{"body_path", "response_path", "unix_socket"}},
}

// rewriteReplacementPaths updates present path arguments in place.
func rewriteReplacementPaths(call ReplacementCall, rewrite func(string) string) {
	fields, ok := replacementPathArgs[call.Tool]
	if !ok {
		return
	}
	for _, key := range fields.single {
		if value, ok := call.Args[key].(string); ok && value != "" {
			call.Args[key] = rewrite(value)
		}
	}
	for _, key := range fields.list {
		items, ok := call.Args[key].([]any)
		if !ok {
			continue
		}
		for i, item := range items {
			if value, ok := item.(string); ok && value != "" {
				items[i] = rewrite(value)
			}
		}
	}
	if fields.pairs != "" {
		items, _ := call.Args[fields.pairs].([]any)
		for _, item := range items {
			pair, _ := item.(map[string]any)
			for _, key := range []string{"from", "to"} {
				if value, ok := pair[key].(string); ok && value != "" {
					pair[key] = rewrite(value)
				}
			}
		}
	}
}

// Relative traversal and shell operands cannot become native paths.
func replacementPathsRepresentable(call ReplacementCall) bool {
	representable := true
	rewriteReplacementPaths(call, func(p string) string {
		if p != strings.TrimSpace(p) {
			representable = false
		}
		if (!filepath.IsAbs(p) || replacementPathArgs[call.Tool].projectOnly) && !projectRelativePath(p) {
			representable = false
		}
		return p
	})
	return representable
}

func projectRelativePath(p string) bool {
	clean := filepath.Clean(p)
	if filepath.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.HasPrefix(p, "@") {
		return false
	}
	return true
}

// joinCommandCwd preserves runner path resolution in native slash form.
func joinCommandCwd(cwd, p string) string {
	if filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	return path.Join(filepath.ToSlash(cwd), filepath.ToSlash(p))
}
