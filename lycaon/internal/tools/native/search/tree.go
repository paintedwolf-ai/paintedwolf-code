package search

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// treeRun is one tree invocation: a listing over the search walk, with the
// read boundary, visit budget and (with --gitignore) the project ignore
// owner applied to every directory it reads.
type treeRun struct {
	inv                *invocation
	walker             *walker
	selection          treeSelection
	full, json         bool
	level              int
	directories, files int
}

// treeSelection decides which walked entries tree lists: -a, -d, -I, -P
// and --gitignore.
type treeSelection struct {
	all, dirsOnly, gitignore bool
	exclude, include         []*regexp.Regexp
	ignores                  ignoreScopes
}

// treeNode is one listed entry and, for a directory below -L, its listed
// children. err marks a directory tree could not open; missing marks an
// operand that does not exist.
type treeNode struct {
	e            entry
	display      string
	target       string
	children     []treeNode
	err, missing bool
}

// tree's UTF-8 branches; the vertical continuation uses no-break spaces.
const (
	treeBranch = "├── "
	treeLast   = "└── "
	treeDown   = "│   "
	treeBlank  = "    "
)

func runTree(inv *invocation) error {
	r := &treeRun{inv: inv, walker: newWalker(inv), full: inv.flag("-f"), json: inv.flag("-J"), level: -1,
		selection: treeSelection{all: inv.flag("-a"), dirsOnly: inv.flag("-d"), gitignore: inv.flag("--gitignore"), ignores: ignoreScopes{}}}
	defer r.walker.close()
	if value, ok := inv.value("-L"); ok {
		level, err := strconv.Atoi(value)
		if err != nil || level < 1 {
			inv.failure = 1
			return inv.usage("", "Invalid level, must be greater than 0.")
		}
		r.level = level
	}
	var err error
	if r.selection.exclude, err = treePatterns(inv, "-I"); err != nil {
		return err
	}
	if r.selection.include, err = treePatterns(inv, "-P"); err != nil {
		return err
	}
	operands := inv.argv.Operands().Values()
	if len(operands) == 0 {
		operands = []string{"."}
	}
	var text strings.Builder
	var roots []string
	for _, operand := range operands {
		root, err := r.root(operand)
		if err != nil {
			return err
		}
		if r.json {
			roots = append(roots, r.jsonNode(root, 1))
		} else {
			r.textRoot(&text, root)
		}
	}
	report := r.report()
	if inv.flag("--noreport") {
		report = ""
	}
	if r.json {
		out := "[\n" + strings.Join(roots, ",\n") + "\n"
		if report != "" {
			out += ",    " + report
		}
		return inv.out.text(out + "\n]\n")
	}
	if report != "" {
		text.WriteString("\n" + report + "\n")
	}
	return inv.out.text(text.String())
}

// treePatterns compiles -I or -P: wildcard patterns joined with |.
func treePatterns(inv *invocation, key string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, value := range inv.values(key) {
		for _, glob := range strings.Split(value, "|") {
			re, err := globRegexp(glob, 0)
			if err != nil {
				return nil, inv.usage(value, "a wildcard pattern")
			}
			out = append(out, re)
		}
	}
	return out, nil
}

// root lists one operand. A missing operand is tree's error entry with
// status 2; a file operand is listed as an unopenable directory.
func (r *treeRun) root(operand string) (treeNode, error) {
	e, err := r.walker.operand(operand, false)
	node := treeNode{e: e, display: operand}
	if err != nil {
		if _, ok := ordinary(err); !ok {
			return node, err
		}
		r.inv.status = max(r.inv.status, 2)
		node.err, node.missing = true, true
		return node, nil
	}
	if !e.dir() {
		r.files++
		node.err = true
		return node, nil
	}
	if node.children, node.err, err = r.list(e, operand, 0); err != nil {
		return node, err
	}
	if len(node.children) > 0 {
		r.directories++
	}
	return node, nil
}

// list reads one directory's admitted children and, below -L, theirs.
func (r *treeRun) list(dir entry, display string, depth int) ([]treeNode, bool, error) {
	children, release, err := r.walker.children(dir, false)
	defer release()
	if err != nil {
		if _, ok := ordinary(err); ok {
			return nil, true, nil
		}
		return nil, false, err
	}
	var out []treeNode
	for _, child := range children {
		if !r.selection.admit(child) {
			continue
		}
		node := treeNode{e: child, display: child.name}
		if r.full {
			node.display = joinDisplay(display, child.name)
		}
		if child.link {
			node.target, _ = child.bound.Readlink()
		}
		if child.dir() {
			r.directories++
			if r.level < 0 || depth+1 < r.level {
				if node.children, node.err, err = r.list(child, joinDisplay(display, child.name), depth+1); err != nil {
					return nil, false, err
				}
			}
		} else {
			r.files++
		}
		out = append(out, node)
	}
	return out, false, nil
}

// admit decides one walked entry.
func (s treeSelection) admit(e entry) bool {
	matches := func(re *regexp.Regexp) bool { return re.MatchString(e.name) }
	switch {
	case !s.all && strings.HasPrefix(e.name, "."):
		return false
	case s.dirsOnly && !e.dir():
		return false
	case slices.ContainsFunc(s.exclude, matches):
		return false
	case len(s.include) > 0 && !e.dir() && !slices.ContainsFunc(s.include, matches):
		return false
	case s.gitignore && !s.ignores.admit(e):
		return false
	}
	return true
}

func (r *treeRun) report() string {
	plural := func(n int, one, many string) string {
		if n == 1 {
			return strconv.Itoa(n) + " " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	if r.json {
		if r.selection.dirsOnly {
			return `{"type":"report","directories":` + strconv.Itoa(r.directories) + "}"
		}
		return `{"type":"report","directories":` + strconv.Itoa(r.directories) + `,"files":` + strconv.Itoa(r.files) + "}"
	}
	if r.selection.dirsOnly {
		return plural(r.directories, "directory", "directories")
	}
	return plural(r.directories, "directory", "directories") + ", " + plural(r.files, "file", "files")
}

func (r *treeRun) textRoot(out *strings.Builder, root treeNode) {
	out.WriteString(root.display)
	if root.err {
		out.WriteString("  [error opening dir]")
	}
	out.WriteByte('\n')
	r.textChildren(out, root.children, "")
}

func (r *treeRun) textChildren(out *strings.Builder, children []treeNode, prefix string) {
	for i, child := range children {
		branch, below := treeBranch, treeDown
		if i == len(children)-1 {
			branch, below = treeLast, treeBlank
		}
		out.WriteString(prefix + branch + child.display)
		if child.e.link {
			out.WriteString(" -> " + child.target)
		}
		if child.err {
			out.WriteString("  [error opening dir]")
		}
		out.WriteByte('\n')
		r.textChildren(out, child.children, prefix+below)
	}
}

// jsonNode renders tree -J's object for one entry at an indentation level.
func (r *treeRun) jsonNode(node treeNode, level int) string {
	indent := strings.Repeat("    ", level)
	kind := "file"
	switch {
	case node.missing:
		kind = "unknown"
	case node.e.link:
		kind = "link"
	case node.e.dir():
		kind = "directory"
	}
	out := indent + `{"type":"` + kind + `","name":` + jsonString(node.display)
	if node.e.link {
		out += `,"target":` + jsonString(node.target)
	}
	switch {
	case node.err:
		return out + `,"contents":[{"error": "error opening dir"}` + "\n" + indent + "]}"
	case len(node.children) == 0:
		return out + "}"
	}
	parts := make([]string, len(node.children))
	for i, child := range node.children {
		parts[i] = r.jsonNode(child, level+1)
	}
	return out + `,"contents":[` + "\n" + strings.Join(parts, ",\n") + "\n" + indent + "]}"
}
