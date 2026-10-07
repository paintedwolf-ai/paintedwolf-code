package maintainability

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"gopkg.in/yaml.v3"
)

type source struct {
	Path, Language string
	Test           bool
	Body           []byte
}

// discoverSources reads the handwritten sources of a checkout.
func discoverSources(tree *workingTree) ([]source, error) {
	generated, err := generatedOutputs(tree)
	if err != nil {
		return nil, err
	}
	var sources []source
	for _, relative := range tree.files() {
		language := languageOf(relative)
		// A tracked deletion or a link is not maintained source.
		if language == "" || excludedPath(relative) || generated[relative] || !tree.regular(relative) {
			continue
		}
		body, err := tree.read(relative)
		if err != nil {
			return nil, fmt.Errorf("read maintained source %s: %w", relative, err)
		}
		sources = append(sources, source{relative, language, testPath(relative), body})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("maintainability source inventory is empty")
	}
	return sources, nil
}

func languageOf(relative string) string {
	switch filepath.Ext(relative) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".sh", ".bash":
		return "bash"
	}
	if relative == "task" {
		return "bash"
	}
	return ""
}

// excludedPath drops upstream material and source-shaped fixture inputs.
func excludedPath(relative string) bool {
	if strings.HasPrefix(relative, "third_party/") || strings.HasPrefix(relative, "lycaon/test/fixtures/") {
		return true
	}
	parts := strings.Split(relative, "/")
	return slices.Contains(parts, "vendor") || slices.Contains(parts, "node_modules") || slices.Contains(parts, "testdata")
}

func testPath(relative string) bool {
	for _, part := range strings.Split(relative, "/") {
		switch part {
		case "test", "tests", "e2e", "testutil", "testcorpus", "testsupport", "testfixture", "verification_tests", "__tests__":
			return true
		}
	}
	name := path.Base(relative)
	return name == "tests.rs" || strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") ||
		strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py") || strings.Contains(name, "_testutil.") || strings.Contains(name, "_testsupport.")
}

// generatedOutputs trusts generator declarations, never filenames or
// self-declared headers, so handwritten code cannot opt out of its budget.
func generatedOutputs(tree *workingTree) (map[string]bool, error) {
	var taskfile struct {
		Tasks map[string]struct {
			Dir       string
			Generates []string
		}
	}
	raw, err := tree.read("Taskfile.yml")
	if err == nil {
		err = yaml.Unmarshal(raw, &taskfile)
	}
	if err != nil {
		return nil, fmt.Errorf("read generator declarations: %w", err)
	}
	out := map[string]bool{}
	for name, task := range taskfile.Tasks {
		if !strings.HasPrefix(name, "codegen:") && name != "db:sqlc" {
			continue
		}
		dir := ""
		switch task.Dir {
		case "":
		case "{{.GO_DIR}}":
			dir = "lycaon"
		default:
			return nil, fmt.Errorf("unrecognized generator directory %q for %s", task.Dir, name)
		}
		for _, pattern := range task.Generates {
			if strings.Contains(pattern, "{{") {
				return nil, fmt.Errorf("unresolved generator output %s: %s", name, pattern)
			}
			matches, err := glob(tree, path.Join(dir, pattern))
			if err != nil {
				return nil, fmt.Errorf("expand generator output %s: %w", pattern, err)
			}
			for _, match := range matches {
				out[match] = true
			}
		}
	}
	// codegen:wire-enums derives its outputs from the vocabulary files.
	vocabulary, err := glob(tree, "docs/openapi/vocab/*.yaml")
	if err != nil {
		return nil, err
	}
	for _, file := range vocabulary {
		var entry struct {
			Name         string
			GoFile       string `yaml:"go_file"`
			StateMachine *struct {
				TSFile string `yaml:"ts_file"`
			} `yaml:"state_machine"`
			TSLabels *struct{ Path string } `yaml:"ts_labels"`
		}
		raw, err := tree.read(file)
		if err == nil {
			err = yaml.Unmarshal(raw, &entry)
		}
		if err != nil {
			return nil, fmt.Errorf("read wire vocabulary %s: %w", file, err)
		}
		if entry.GoFile == "" {
			var snake strings.Builder
			for i, r := range entry.Name {
				if r >= 'A' && r <= 'Z' {
					if i > 0 {
						snake.WriteByte('_')
					}
					r += 'a' - 'A'
				}
				snake.WriteRune(r)
			}
			entry.GoFile = snake.String() + "_ids.generated.go"
		}
		out["lycaon/pkg/api/"+entry.GoFile] = true
		if entry.StateMachine != nil {
			out[entry.StateMachine.TSFile] = true
		}
		if entry.TSLabels != nil {
			out[entry.TSLabels.Path] = true
		}
		if entry.Name == "EventTopic" {
			out["lycaon-den/src/api/event-topics.generated.ts"] = true
			out["lycaon-den/src/api/event-payloads.generated.ts"] = true
		}
	}
	return out, nil
}

type inventory struct {
	measured measurements
	// sources lists the files declaring each Go type artifact.
	sources map[string][]string
	// declarations and methods locate each Go type's struct declaration and
	// receiver methods, so a change touches a type's fields only when it edits
	// the declaration, and its methods only when it edits a method.
	declarations map[string][]span
	methodSpans  map[string][]span
	methods      map[string]map[string]bool
}

// span is an inclusive line range in one file.
type span struct {
	file        string
	first, last int
}

func measure(ctx context.Context, tree *workingTree, sources []source) (*inventory, error) {
	inv := &inventory{measured: newMeasurements(), sources: map[string][]string{}, declarations: map[string][]span{}, methodSpans: map[string][]span{}, methods: map[string]map[string]bool{}}
	for _, src := range sources {
		var lines map[int]bool
		var err error
		if src.Language == "go" {
			lines, err = inv.measureGo(src)
		} else {
			var imports map[string]bool
			lines, imports, err = measureTree(ctx, tree, src)
			if imports != nil {
				inv.measured["ts_local_dependencies"][src.Path] = len(imports)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("measure %s: %w", src.Path, err)
		}
		files, directories := "source_files", "source_directories"
		if src.Test {
			files, directories = "test_files", "test_directories"
		}
		inv.measured[files][src.Path] = len(lines)
		inv.measured[directories][path.Dir(src.Path)]++
	}
	for id, names := range inv.methods {
		inv.measured["go_receiver_methods"][id] = len(names)
	}
	return inv, nil
}

// measureGo counts code-bearing lines and, for production files, accumulates
// struct fields and receiver methods by package type across files.
func (inv *inventory) measureGo(src source) (map[int]bool, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, src.Path, src.Body, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse Go source: %w", err)
	}
	file := fset.File(parsed.Pos())
	lines := map[int]bool{}
	var scan scanner.Scanner
	scan.Init(file, src.Body, nil, 0)
	for {
		pos, tok, literal := scan.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && literal == "\n" {
			continue
		}
		start := file.Line(pos)
		for row := start; row <= start+strings.Count(literal, "\n"); row++ {
			lines[row] = true
		}
	}
	if src.Test {
		return lines, nil
	}
	prefix := path.Dir(src.Path) + "/" + parsed.Name.Name + "."
	for _, declaration := range parsed.Decls {
		switch decl := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				named, ok := spec.(*ast.TypeSpec)
				if !ok || named.Assign.IsValid() {
					continue
				}
				structure, ok := named.Type.(*ast.StructType)
				if !ok {
					continue
				}
				fields := 0
				for _, field := range structure.Fields.List {
					fields += max(1, len(field.Names))
				}
				id := prefix + named.Name.Name
				// Build-variant declarations of one type count once, at their largest.
				inv.measured["go_struct_fields"][id] = max(inv.measured["go_struct_fields"][id], fields)
				inv.addSource(id, src.Path)
				inv.declarations[id] = append(inv.declarations[id], span{src.Path, file.Line(named.Pos()), file.Line(named.End())})
			}
		case *ast.FuncDecl:
			if decl.Recv == nil {
				continue
			}
			id := prefix + receiverName(decl.Recv.List[0].Type)
			if inv.methods[id] == nil {
				inv.methods[id] = map[string]bool{}
			}
			inv.methods[id][decl.Name.Name] = true
			for row := file.Line(decl.Pos()); row <= file.Line(decl.End()); row++ {
				if lines[row] {
					inv.measured["go_receiver_lines"][id]++
				}
			}
			inv.addSource(id, src.Path)
			inv.methodSpans[id] = append(inv.methodSpans[id], span{src.Path, file.Line(decl.Pos()), file.Line(decl.End())})
		}
	}
	return lines, nil
}

// addSource relies on sources arriving in path order.
func (inv *inventory) addSource(id, file string) {
	if files := inv.sources[id]; len(files) == 0 || files[len(files)-1] != file {
		inv.sources[id] = append(files, file)
	}
}

func receiverName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e.Name
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			return ""
		}
	}
}

// measureTree counts code-bearing lines with tree-sitter; production
// TypeScript also reports its resolved local imports (nil otherwise).
func measureTree(ctx context.Context, files *workingTree, src source) (map[int]bool, map[string]bool, error) {
	entry := grammars.DetectLanguageByName(src.Language)
	if entry == nil {
		return nil, nil, fmt.Errorf("no grammar for %s", src.Language)
	}
	language := entry.Language()
	tree, err := tsparse.ParseWithin(ctx, language, src.Body, 10*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", src.Language, err)
	}
	defer tree.Release()
	if tree.RootNode() == nil || tree.RootNode().HasErrorOrMissing() {
		return nil, nil, fmt.Errorf("%s source contains a syntax error or missing token", src.Language)
	}
	lines := map[int]bool{}
	var imports map[string]bool
	if !src.Test && (src.Language == "typescript" || src.Language == "tsx") {
		imports = map[string]bool{}
	}
	var visit func(*gotreesitter.Node) error
	visit = func(node *gotreesitter.Node) error {
		kind := node.Type(language)
		if kind == "comment" || kind == "line_comment" || kind == "block_comment" || kind == "hash_bang_line" {
			return nil
		}
		if imports != nil {
			if specifier := importSpecifier(node, language, src.Body); strings.HasPrefix(specifier, ".") {
				resolved, err := resolveImport(files, src.Path, specifier)
				if err != nil {
					return err
				}
				imports[resolved] = true
			}
		}
		literal := kind == "string" || kind == "string_literal" || kind == "raw_string_literal" || kind == "template_string" || kind == "heredoc_body"
		if literal || node.ChildCount() == 0 {
			start, end := int(node.StartByte()), int(node.EndByte())
			if end > start && strings.TrimSpace(string(src.Body[start:end])) != "" {
				last := int(node.EndPoint().Row) + 1
				if node.EndPoint().Column == 0 {
					last--
				}
				for row := int(node.StartPoint().Row) + 1; row <= last; row++ {
					lines[row] = true
				}
			}
			// Literals are descended because template substitutions may import.
			if !literal {
				return nil
			}
		}
		for i := 0; i < node.ChildCount(); i++ {
			if err := visit(node.Child(i)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(tree.RootNode()); err != nil {
		return nil, nil, err
	}
	return lines, imports, nil
}

// importSpecifier returns the literal module of an import, re-export, or
// dynamic import; computed specifiers return "".
func importSpecifier(node *gotreesitter.Node, language *gotreesitter.Language, body []byte) string {
	var value *gotreesitter.Node
	switch node.Type(language) {
	case "import_statement", "export_statement", "import_require_clause":
		value = node.ChildByFieldName("source", language)
	case "call_expression":
		function, args := node.ChildByFieldName("function", language), node.ChildByFieldName("arguments", language)
		if function == nil || args == nil || string(body[function.StartByte():function.EndByte()]) != "import" {
			return ""
		}
		for i := 0; i < args.ChildCount() && value == nil; i++ {
			if kind := args.Child(i).Type(language); kind == "string" || kind == "template_string" {
				value = args.Child(i)
			}
		}
	}
	if value == nil {
		return ""
	}
	for i := 0; i < value.ChildCount(); i++ {
		if value.Child(i).Type(language) == "template_substitution" {
			return ""
		}
	}
	raw := string(body[value.StartByte():value.EndByte()])
	if len(raw) < 2 {
		return ""
	}
	return raw[1 : len(raw)-1]
}

// resolveImport follows TypeScript bundler resolution to a checkout file.
func resolveImport(tree *workingTree, from, specifier string) (string, error) {
	specifier, _, _ = strings.Cut(specifier, "?")
	base := path.Join(path.Dir(from), specifier)
	if base == ".." || strings.HasPrefix(base, "../") {
		return "", fmt.Errorf("local import %q escapes repository", specifier)
	}
	var candidates []string
	if stem, ok := strings.CutSuffix(base, ".js"); ok {
		candidates = append(candidates, stem+".ts", stem+".tsx")
	}
	candidates = append(candidates, base)
	for _, ext := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".json"} {
		candidates = append(candidates, base+ext, base+"/index"+ext)
	}
	for _, candidate := range candidates {
		if tree.regular(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unresolved local import %q from %s", specifier, from)
}
