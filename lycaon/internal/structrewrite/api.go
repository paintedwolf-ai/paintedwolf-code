package structrewrite

// WalkRequest fans out structural search over a tree.
type WalkRequest struct {
	Root, Pattern string
	Subpaths      []string // focus paths; empty = whole project
	Recursive     bool
	LangName      string
	MaxFiles      int
	MaxMatches    int
	// PathIncluded optionally filters repo-relative paths (read scope, gitignore, …).
	PathIncluded func(relSlash string, isDir bool) bool
}

// FileMatches holds matches for one file in a walk.
type FileMatches struct {
	Path, Language string
	Matches        []Match
}

// SymbolRef locates a named definition node.
type SymbolRef struct {
	Name string
	Kind string // optional disambiguator
}
