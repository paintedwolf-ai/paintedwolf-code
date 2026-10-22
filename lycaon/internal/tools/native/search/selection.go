package search

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"mvdan.cc/sh/v3/pattern"
)

// nameRule is one ordered --include or --exclude glob.
type nameRule struct {
	include bool
	glob    *regexp.Regexp
}

// selection decides which walked entries a search visits.
//
// grep: --include/--exclude follow GNU's ordered rule (the last matching
// glob wins; with none matching a file is searched unless the first rule is
// --include) against the base name of a walked file, or any trailing name
// of a command-line file. --exclude-dir prunes walked and command-line
// directories. Hidden entries are visited and no ignore file applies.
//
// rg: hidden entries are skipped and the project ignore owner applies;
// -u drops ignore files, -uu also visits hidden entries (as --hidden does),
// -uuu also searches binary files. -g globs match a base name, or the path
// below the search root when they hold a "/"; "!" excludes. -t selects
// catalogued file types. Command-line paths are always searched.
type selection struct {
	inv          *invocation
	rg           bool
	rules        []nameRule
	excludeDirs  []*regexp.Regexp
	includeDirs  []*regexp.Regexp
	globs        []rgGlob
	types        []*regexp.Regexp
	notTypes     []*regexp.Regexp
	noIgnore     bool
	unrestricted int
	hidden       bool
	scopes       map[string]*sourcescope.Scope
	err          error
}

type rgGlob struct {
	exclude  bool
	anchored bool
	glob     *regexp.Regexp
}

func newSelection(inv *invocation, rg bool) selection {
	s := selection{inv: inv, rg: rg, scopes: map[string]*sourcescope.Scope{}}
	for _, item := range inv.argv.Items {
		if item.Option == nil || len(item.Option.Values) == 0 && item.Option.Key != "-u" {
			continue
		}
		switch item.Option.Key {
		case "--include", "--exclude":
			s.rules = append(s.rules, nameRule{include: item.Option.Key == "--include", glob: s.compile(item.Option.Values[0].Value, 0)})
		case "--exclude-dir":
			s.excludeDirs = append(s.excludeDirs, s.compile(item.Option.Values[0].Value, 0))
		case "--include-dir":
			s.includeDirs = append(s.includeDirs, s.compile(item.Option.Values[0].Value, 0))
		case "-u":
			s.unrestricted++
		}
	}
	s.hidden = !rg || s.unrestricted >= 2 || inv.flag("--hidden")
	s.noIgnore = rg && inv.flag("--no-ignore")
	return s
}

func (s *selection) compile(glob string, mode pattern.Mode) *regexp.Regexp {
	expr, err := pattern.Regexp(glob, pattern.EntireString|mode)
	if err == nil {
		var re *regexp.Regexp
		if re, err = regexp.Compile(expr); err == nil {
			return re
		}
	}
	if s.err == nil {
		s.err = s.inv.usage(glob, "a valid shell glob")
	}
	return regexp.MustCompile(`\A\z.`)
}

// compileRg binds -g and -t.
func (s *selection) compileRg() error {
	for _, glob := range s.inv.values("-g") {
		g := rgGlob{exclude: strings.HasPrefix(glob, "!")}
		glob = strings.TrimPrefix(glob, "!")
		g.anchored = strings.Contains(strings.TrimSuffix(glob, "/"), "/")
		g.glob = s.compile(strings.TrimPrefix(glob, "/"), pattern.Filenames|pattern.GlobLeadingDot)
		s.globs = append(s.globs, g)
	}
	var err error
	if s.types, err = s.typeGlobs("-t"); err != nil {
		return err
	}
	if s.notTypes, err = s.typeGlobs("-T"); err != nil {
		return err
	}
	return s.err
}

func (s *selection) typeGlobs(key string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, name := range s.inv.values(key) {
		globs, ok := rgTypes[name]
		if !ok {
			return nil, s.inv.usage(name, "a catalogued file type ("+strings.Join(rgTypeNames(), ", ")+")")
		}
		for _, glob := range globs {
			out = append(out, s.compile(glob, pattern.GlobLeadingDot))
		}
	}
	return out, nil
}

// commandLineFile applies grep's ordered name rules to an operand's trailing
// names; rg searches every named file.
func (s *selection) commandLineFile(operand string) bool {
	if s.rg {
		return true
	}
	suffixes := trailingNames(operand)
	return s.included(func(re *regexp.Regexp) bool { return slices.ContainsFunc(suffixes, re.MatchString) })
}

func (s *selection) commandLineDir(operand string) bool {
	if s.rg || operand == "." {
		return true
	}
	for _, name := range trailingNames(strings.TrimRight(operand, "/")) {
		for _, re := range s.excludeDirs {
			if re.MatchString(name) {
				return false
			}
		}
	}
	return true
}

func (s *selection) included(match func(*regexp.Regexp) bool) bool {
	decided := len(s.rules) == 0 || !s.rules[0].include
	for _, rule := range s.rules {
		if match(rule.glob) {
			decided = rule.include
		}
	}
	return decided
}

// admit decides one walked entry.
func (s *selection) admit(e entry) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if !s.hidden && strings.HasPrefix(e.name, ".") {
		return false, nil
	}
	if e.dir() {
		for _, re := range s.excludeDirs {
			if re.MatchString(e.name) {
				return false, nil
			}
		}
		if len(s.includeDirs) > 0 && !slices.ContainsFunc(s.includeDirs, func(re *regexp.Regexp) bool { return re.MatchString(e.name) }) {
			return false, nil
		}
	} else if !s.included(func(re *regexp.Regexp) bool { return re.MatchString(e.name) }) {
		return false, nil
	}
	if !s.rg {
		return true, nil
	}
	if s.unrestricted == 0 && !s.noIgnore && !s.ignoreAdmits(e) {
		return false, nil
	}
	return s.rgGlobsAdmit(e), nil
}

func (s *selection) rgGlobsAdmit(e entry) bool {
	rel := strings.TrimPrefix(e.display, "./")
	includes := false
	admitted := false
	for _, g := range s.globs {
		subject := e.name
		if g.anchored {
			subject = rel
		}
		if g.exclude {
			if g.glob.MatchString(subject) {
				return false
			}
			continue
		}
		includes = true
		if !e.dir() && g.glob.MatchString(subject) {
			admitted = true
		}
	}
	if includes && !e.dir() && !admitted {
		return false
	}
	if e.dir() {
		return true
	}
	matches := func(re *regexp.Regexp) bool { return re.MatchString(e.name) }
	if slices.ContainsFunc(s.notTypes, matches) {
		return false
	}
	return len(s.types) == 0 || slices.ContainsFunc(s.types, matches)
}

// ignoreAdmits applies the project ignore owner below the entry's root.
func (s *selection) ignoreAdmits(e entry) bool {
	root := e.bound.Resolved.Root.Path
	if root == "" || e.bound.Resolved.External {
		return true
	}
	scope, ok := s.scopes[root]
	if !ok {
		scope = sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{IgnoreFiles: true}})
		s.scopes[root] = scope
	}
	return scope.AdmitPath(e.bound.Resolved.ScopeRel, e.dir())
}

func trailingNames(operand string) []string {
	out := []string{operand}
	for i := 0; i < len(operand); i++ {
		if operand[i] == '/' && i+1 < len(operand) {
			out = append(out, operand[i+1:])
		}
	}
	return out
}

// rgTypes is the catalogued file type table for rg -t.
var rgTypes = map[string][]string{
	"c": {"*.c", "*.h"}, "cpp": {"*.cpp", "*.cc", "*.cxx", "*.c++", "*.hpp", "*.hh", "*.hxx", "*.h", "*.inl"},
	"css": {"*.css", "*.scss", "*.sass", "*.less"}, "docker": {"Dockerfile", "Dockerfile.*", "*.dockerfile"},
	"go": {"*.go"}, "html": {"*.html", "*.htm", "*.xhtml"}, "java": {"*.java"},
	"js": {"*.js", "*.jsx", "*.mjs", "*.cjs", "*.vue"}, "json": {"*.json", "*.jsonl", "*.json5"},
	"kotlin": {"*.kt", "*.kts"}, "make": {"Makefile", "makefile", "GNUmakefile", "*.mk", "*.mak"},
	"markdown": {"*.md", "*.markdown", "*.mdx", "*.mkd", "*.mkdn"}, "md": {"*.md", "*.markdown", "*.mdx", "*.mkd", "*.mkdn"},
	"proto": {"*.proto"}, "py": {"*.py", "*.pyi"}, "ruby": {"*.rb", "*.gemspec", "Gemfile", "Rakefile"},
	"rust": {"*.rs"}, "sh": {"*.sh", "*.bash", "*.zsh", "*.ksh", ".bashrc", ".zshrc", ".profile"},
	"sql": {"*.sql"}, "swift": {"*.swift"}, "toml": {"*.toml", "Cargo.lock"},
	"ts": {"*.ts", "*.tsx", "*.mts", "*.cts"}, "txt": {"*.txt"}, "yaml": {"*.yaml", "*.yml"},
}

func rgTypeNames() []string {
	names := make([]string, 0, len(rgTypes))
	for name := range rgTypes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
