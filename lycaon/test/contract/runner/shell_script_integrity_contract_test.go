package contract

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Vendored code and test inputs are not first-party executable scripts.
var shellInventoryExcludedSegments = map[string]bool{
	"testdata": true, "fixtures": true, "vendor": true, "third_party": true, "node_modules": true,
}

var shellShebang = regexp.MustCompile(`^#!\s*(?:/usr/bin/env\s+)?(?:\S*/)?(?:bash|sh)(?:\s|$)`)

// shellRepo is the checkout's file inventory and its first-party shell scripts.
type shellRepo struct {
	root    string
	files   map[string]bool
	scripts []string
}

func loadShellRepo(t *testing.T) shellRepo {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	raw, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	contractcheck.FailErr(t, "list checkout files", err)
	repo := shellRepo{root: root, files: map[string]bool{}}
	for _, rel := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		repo.files[rel] = true
		if slices.ContainsFunc(strings.Split(path.Dir(rel), "/"), func(s string) bool { return shellInventoryExcludedSegments[s] }) {
			continue
		}
		if isShellScript(filepath.Join(root, filepath.FromSlash(rel)), rel) {
			repo.scripts = append(repo.scripts, rel)
		}
	}
	slices.Sort(repo.scripts)
	if len(repo.scripts) == 0 {
		t.Fatal("no shell scripts found; the inventory walk is broken")
	}
	return repo
}

func isShellScript(absolute, rel string) bool {
	switch ext := path.Ext(rel); {
	case ext == ".sh" || ext == ".bash" || path.Base(rel) == ".envrc":
		return true
	case ext != "":
		return false
	}
	file, err := os.Open(absolute)
	if err != nil {
		return false
	}
	defer file.Close()
	head := make([]byte, 128)
	n, _ := file.Read(head)
	line, _, _ := bytes.Cut(head[:n], []byte("\n"))
	return shellShebang.Match(line)
}

func (r shellRepo) read(t *testing.T, rel string) string {
	t.Helper()
	return contractcheck.ReadRepoFile(t, r.root, rel)
}

func TestShellScriptsParse(t *testing.T) {
	t.Parallel()
	repo := loadShellRepo(t)
	var violations []string
	for _, rel := range repo.scripts {
		out, err := exec.CommandContext(t.Context(), "bash", "-n", filepath.Join(repo.root, filepath.FromSlash(rel))).CombinedOutput()
		if err != nil {
			violations = append(violations, rel+": "+strings.TrimSpace(strings.ReplaceAll(string(out), repo.root+string(filepath.Separator), "")))
		}
		for _, edge := range shellSourceEdges(repo, rel, repo.read(t, rel)) {
			if edge.missing {
				violations = append(violations, rel+":"+strconv.Itoa(edge.line)+": sources "+edge.raw+", which resolves to no file in the checkout")
			}
		}
	}
	contractcheck.FailViolations(t,
		"shell scripts must parse with `bash -n` and source only files that exist; fix the syntax error or the sourced path", violations)
}

// shellSourceEdge is one `source`/`.` statement and the checkout file it names.
type shellSourceEdge struct {
	line    int
	raw     string
	target  string
	missing bool
}

var (
	shellSourceCommand   = regexp.MustCompile(`(?:^|[;&|]\s*|\b(?:then|do|else)\s+)(?:source|\.)\s+`)
	shellSingleVariable  = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	shellLeadingVariable = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?(/.*)?$`)
)

// shellSourceEdges resolves each sourced script by its literal suffix, searched
// from the script's directory upward, so `${ROOT}/scripts/x.sh`,
// `$(dirname "$0")/x.sh`, and `${DIR}/e2e/x.sh` all name checkout files.
// Targets that are not scripts (session env files, `$1`) are runtime inputs.
func shellSourceEdges(repo shellRepo, rel, body string) []shellSourceEdge {
	var edges []shellSourceEdge
	for index, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, loc := range shellSourceCommand.FindAllStringIndex(trimmed, -1) {
			raw := shellWord(trimmed[loc[1]:])
			tail := strings.TrimPrefix(path.Clean("/"+shellResolvedTail(body, raw, 0)), "/")
			if ext := path.Ext(tail); ext != ".sh" && ext != ".bash" {
				continue
			}
			edge := shellSourceEdge{line: index + 1, raw: raw}
			for dir := path.Dir(rel); ; dir = path.Dir(dir) {
				if candidate := path.Join(dir, tail); repo.files[candidate] {
					edge.target = candidate
					break
				}
				if dir == "." {
					break
				}
			}
			edge.missing = edge.target == ""
			edges = append(edges, edge)
		}
	}
	return edges
}

// shellWord reads one shell word, honoring quotes and $(...) nesting.
func shellWord(s string) string {
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++
		case quote == '"' && depth == 0:
			if c == '"' {
				quote = 0
			} else if c == '$' && i+1 < len(s) && s[i+1] == '(' {
				depth++
				i++
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			depth++
			i++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t' || c == ';' || c == '&' || c == '|' || c == ')':
			return s[:i]
		}
	}
	return s
}

// shellResolvedTail is the checkout-relative literal suffix of a sourced
// word: a leading `$VAR/` expands through VAR's assignment in the script, and
// any other expansion keeps only the literal text after it.
func shellResolvedTail(body, raw string, depth int) string {
	unquoted := strings.NewReplacer(`"`, "", `'`, "").Replace(raw)
	if match := shellLeadingVariable.FindStringSubmatch(unquoted); match != nil && depth < 4 {
		if value := shellAssignment(body, match[1]); value != "" {
			return shellResolvedTail(body, value, depth+1) + match[2]
		}
		return match[2]
	}
	last := max(strings.LastIndexAny(unquoted, "})"), -1)
	if dollar := strings.LastIndex(unquoted, "$"); dollar > last {
		end := dollar + 1
		for end < len(unquoted) && isScriptNameByte(unquoted[end]) && unquoted[end] != '.' && unquoted[end] != '-' {
			end++
		}
		last = end - 1
	}
	return unquoted[last+1:]
}

func shellAssignment(body, name string) string {
	assignment := regexp.MustCompile(`(?m)^\s*(?:export\s+|local\s+|readonly\s+)?` + regexp.QuoteMeta(name) + `=(.*)$`)
	if match := assignment.FindStringSubmatch(body); match != nil {
		return shellWord(match[1])
	}
	return ""
}

// shellSourceClosure lists every file a script transitively sources.
func shellSourceClosure(t *testing.T, repo shellRepo, rel string) []string {
	t.Helper()
	seen := map[string]bool{}
	var visit func(string)
	visit = func(script string) {
		for _, edge := range shellSourceEdges(repo, script, repo.read(t, script)) {
			if edge.target == "" || seen[edge.target] {
				continue
			}
			seen[edge.target] = true
			visit(edge.target)
		}
	}
	visit(rel)
	delete(seen, rel)
	closure := make([]string, 0, len(seen))
	for target := range seen {
		closure = append(closure, target)
	}
	slices.Sort(closure)
	return closure
}

func TestScriptFixturesProvideSourcedFiles(t *testing.T) {
	t.Parallel()
	repo := loadShellRepo(t)
	shell := map[string]bool{}
	for _, rel := range repo.scripts {
		shell[rel] = true
	}
	corpus := loadFixtureCorpus(t, filepath.Join(repo.root, "lycaon", "test"))
	var violations []string
	checked := 0
	for _, file := range corpus.files {
		facts := corpus.reach(file)
		for _, copied := range facts.copies {
			// A script named only where it is copied is fixture data (for
			// example a Taskfile source); one the fixture names again runs.
			if !shell[copied] || scriptMentions(corpus.facts[file].literals, copied) < 2 {
				continue
			}
			checked++
			for _, dependency := range shellSourceClosure(t, repo, copied) {
				if !facts.provides(dependency) {
					violations = append(violations, corpus.relative(file)+": copies "+copied+" into a fixture root, but "+copied+" sources "+dependency+", which the fixture never provides")
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no fixture copies a repository shell script; the copy detector is broken")
	}
	contractcheck.FailViolations(t,
		"a fixture that copies a repository script into a temp root must also provide every file that script sources; copy or stub the sourced file in the fixture", violations)
}

// fixtureFacts are the repository scripts a Go file copies into fixture roots
// and every string literal it (or a helper it calls) mentions.
type fixtureFacts struct {
	copies   []string
	copyAll  bool
	literals []string
}

func (f fixtureFacts) provides(rel string) bool {
	return f.copyAll || slices.Contains(f.copies, rel) || scriptMentions(f.literals, rel) > 0
}

// scriptMentions counts whole-name occurrences of a script's base name.
func scriptMentions(literals []string, rel string) int {
	base, count := path.Base(rel), 0
	for _, literal := range literals {
		for offset := 0; ; {
			i := strings.Index(literal[offset:], base)
			if i < 0 {
				break
			}
			start, end := offset+i, offset+i+len(base)
			if (start == 0 || !isScriptNameByte(literal[start-1])) && (end == len(literal) || !isScriptNameByte(literal[end])) {
				count++
			}
			offset = start + 1
		}
	}
	return count
}

func isScriptNameByte(b byte) bool {
	return b == '-' || b == '_' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

type fixtureCorpus struct {
	root  string
	files []string
	facts map[string]fixtureFacts
	// calls maps a file or helper to the helpers it calls; helpers are keyed
	// "dir#Name" for their package directory.
	calls   map[string][]string
	helpers map[string]string
}

func loadFixtureCorpus(t *testing.T, root string) fixtureCorpus {
	t.Helper()
	corpus := fixtureCorpus{root: root, facts: map[string]fixtureFacts{}, calls: map[string][]string{}, helpers: map[string]string{}}
	checkDir := filepath.Join(root, "contract", "internal", "check")
	type parsed struct {
		path string
		file *ast.File
	}
	var parsedFiles []parsed
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		parsedFiles = append(parsedFiles, parsed{p, file})
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				corpus.helpers[filepath.Dir(p)+"#"+fn.Name.Name] = p + "#" + fn.Name.Name
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "parse Go fixtures", err)
	for _, entry := range parsedFiles {
		dir := filepath.Dir(entry.path)
		checkAlias := ""
		for _, imp := range entry.file.Imports {
			if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/test/contract/internal/check") {
				checkAlias = "check"
				if imp.Name != nil {
					checkAlias = imp.Name.Name
				}
			}
		}
		localWriters := fixtureWriterFuncs(entry.file)
		collect := func(key string, node ast.Node) {
			facts := corpus.facts[key]
			walkFixtureNode(node, func(n ast.Node, stack []ast.Node) {
				switch n := n.(type) {
				case *ast.BasicLit:
					if value, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING {
						facts.literals = append(facts.literals, value)
						copies, all := shellCopiedScripts(value)
						facts.copies = append(facts.copies, copies...)
						facts.copyAll = facts.copyAll || all
					}
				case *ast.CallExpr:
					if callee := calleeHelper(n, dir, checkAlias, checkDir); callee != "" {
						corpus.calls[key] = append(corpus.calls[key], callee)
					}
					if name := contractcheck.CallFuncName(n.Fun); (name == "ReadRepoFile" || name == "ReadFile") && len(n.Args) > 0 && flowsIntoFixture(stack, localWriters) {
						for _, source := range resolveFixturePath(n.Args[len(n.Args)-1], stack) {
							if rel := scriptsRelative(source); rel != "" {
								facts.copies = append(facts.copies, rel)
							}
						}
					}
				}
			})
			corpus.facts[key] = facts
		}
		corpus.files = append(corpus.files, entry.path)
		collect(entry.path, entry.file)
		for _, decl := range entry.file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				collect(entry.path+"#"+fn.Name.Name, fn)
			}
		}
	}
	slices.Sort(corpus.files)
	return corpus
}

// reach unions a file's facts with those of every helper it calls, transitively.
func (c fixtureCorpus) reach(file string) fixtureFacts {
	var out fixtureFacts
	seen := map[string]bool{}
	var visit func(string)
	visit = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		facts := c.facts[key]
		out.copies = append(out.copies, facts.copies...)
		out.literals = append(out.literals, facts.literals...)
		out.copyAll = out.copyAll || facts.copyAll
		for _, callee := range c.calls[key] {
			if helper, ok := c.helpers[callee]; ok {
				visit(helper)
			}
		}
	}
	visit(file)
	slices.Sort(out.copies)
	out.copies = slices.Compact(out.copies)
	return out
}

func (c fixtureCorpus) relative(file string) string {
	rel, err := filepath.Rel(filepath.Dir(c.root), file)
	if err != nil {
		return file
	}
	return "lycaon/" + filepath.ToSlash(rel)
}

func calleeHelper(call *ast.CallExpr, dir, checkAlias, checkDir string) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return dir + "#" + fun.Name
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok && checkAlias != "" && pkg.Name == checkAlias {
			return checkDir + "#" + fun.Sel.Name
		}
	}
	return ""
}

// fixtureWriterFuncs names the file's functions that write files.
func fixtureWriterFuncs(file *ast.File) map[string]bool {
	writers := map[string]bool{"WriteFile": true}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && contractcheck.CallFuncName(call.Fun) == "WriteFile" {
				writers[fn.Name.Name] = true
			}
			return true
		})
	}
	return writers
}

// flowsIntoFixture reports whether a read's content is written somewhere: it
// is a composite-literal value (a fixture file map) or an argument to a writer.
func flowsIntoFixture(stack []ast.Node, writers map[string]bool) bool {
	child := stack[len(stack)-1]
	for i := len(stack) - 2; i >= 0; i-- {
		switch parent := stack[i].(type) {
		case *ast.CallExpr:
			if _, conversion := parent.Fun.(*ast.ArrayType); conversion || contractcheck.CallFuncName(parent.Fun) == "string" {
				child = parent
				continue
			}
			return parent.Fun != child && writers[contractcheck.CallFuncName(parent.Fun)]
		case *ast.KeyValueExpr:
			return parent.Value == child
		case *ast.CompositeLit:
			return true
		case *ast.ParenExpr:
			child = parent
			continue
		}
		return false
	}
	return false
}

// resolveFixturePath expands a path expression built from literals, joins,
// concatenation, and range variables over literal string slices.
func resolveFixturePath(expr ast.Expr, stack []ast.Node) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if value, err := strconv.Unquote(e.Value); err == nil && e.Kind == token.STRING {
			return []string{value}
		}
	case *ast.ParenExpr:
		return resolveFixturePath(e.X, stack)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil
		}
		var out []string
		for _, left := range resolveFixturePath(e.X, stack) {
			for _, right := range resolveFixturePath(e.Y, stack) {
				out = append(out, left+right)
			}
		}
		return out
	case *ast.Ident:
		for i := len(stack) - 1; i >= 0; i-- {
			rng, ok := stack[i].(*ast.RangeStmt)
			if !ok {
				continue
			}
			if value, ok := rng.Value.(*ast.Ident); !ok || value.Name != e.Name {
				continue
			}
			lit, ok := rng.X.(*ast.CompositeLit)
			if !ok {
				return nil
			}
			var out []string
			for _, elt := range lit.Elts {
				out = append(out, resolveFixturePath(elt, nil)...)
			}
			return out
		}
	case *ast.CallExpr:
		if name := contractcheck.CallFuncName(e.Fun); name != "Join" {
			return nil
		}
		joined := []string{""}
		for _, arg := range e.Args {
			parts := resolveFixturePath(arg, stack)
			if len(parts) == 0 {
				continue
			}
			var next []string
			for _, prefix := range joined {
				for _, part := range parts {
					next = append(next, path.Join(prefix, part))
				}
			}
			joined = next
		}
		return joined
	}
	return nil
}

func scriptsRelative(p string) string {
	clean := path.Clean("/" + filepath.ToSlash(p))
	index := strings.LastIndex(clean, "/scripts/")
	if index < 0 {
		return ""
	}
	return clean[index+1:]
}

var (
	shellForList   = regexp.MustCompile(`\bfor\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+([^;\n]*)(?:;|\n)\s*do\b`)
	shellCopyToken = regexp.MustCompile(`"[^"]*"|'[^']*'|\S+`)
)

// shellCopiedScripts lists repository scripts an embedded shell fixture copies
// with `cp`, expanding `for name in ...` loop variables; all reports a copy of
// the whole scripts directory.
func shellCopiedScripts(body string) (copies []string, all bool) {
	loops := map[string][]string{}
	for _, match := range shellForList.FindAllStringSubmatch(body, -1) {
		loops[match[1]] = strings.Fields(match[2])
	}
	for _, line := range strings.Split(body, "\n") {
		fields := shellCopyToken.FindAllString(strings.TrimSpace(line), -1)
		if len(fields) < 3 || fields[0] != "cp" {
			continue
		}
		for _, field := range fields[1 : len(fields)-1] {
			token := strings.Trim(field, `"'`)
			index := strings.LastIndex(token, "scripts")
			if index < 0 || strings.HasPrefix(token, "-") {
				continue
			}
			tail := strings.TrimPrefix(token[index+len("scripts"):], "/")
			if tail == "" {
				all = true
				continue
			}
			if variable := shellSingleVariable.FindStringSubmatch(tail); variable != nil {
				for _, item := range loops[variable[1]] {
					copies = append(copies, "scripts/"+item)
				}
				continue
			}
			if !strings.Contains(tail, "$") {
				copies = append(copies, "scripts/"+tail)
			}
		}
	}
	return copies, all
}

// walkFixtureNode visits every node with its ancestors, innermost last.
func walkFixtureNode(root ast.Node, visit func(ast.Node, []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		visit(n, stack)
		return true
	})
}
