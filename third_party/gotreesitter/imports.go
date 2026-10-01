package gotreesitter

import (
	goparser "go/parser"
	gotoken "go/token"
	"strconv"
	"strings"
)

// ImportRef is a compact language-neutral dependency declaration extracted
// from a syntax tree.
type ImportRef struct {
	Lang      string
	Kind      string
	Path      string
	From      string
	Name      string
	Alias     string
	Static    bool
	Wildcard  bool
	Relative  int
	StartByte uint32
	EndByte   uint32
}

// ImportExtractStatus describes the confidence of source-only import
// extraction.
type ImportExtractStatus string

const (
	ImportExtractOK                   ImportExtractStatus = "ok"
	ImportExtractUnsupportedConstruct ImportExtractStatus = "unsupported_construct"
	ImportExtractScannerError         ImportExtractStatus = "scanner_error"
	ImportExtractAmbiguous            ImportExtractStatus = "ambiguous"
	ImportExtractFallbackToTree       ImportExtractStatus = "fallback_to_tree"
)

// ImportExtractResult is returned by source-only dependency extraction. When
// FallbackRecommended is true, callers that need exact tree-sitter behavior
// should parse the file and use ExtractImports.
type ImportExtractResult struct {
	Imports             []ImportRef
	Status              ImportExtractStatus
	Reason              string
	FallbackRecommended bool
}

// ExtractImports returns package/import declarations for the languages used by
// Gazelle-style dependency extraction. It is intentionally independent from the
// generic query engine so it can later be backed by compact parser refs.
func ExtractImports(tree *Tree) []ImportRef {
	if tree == nil || tree.RootNode() == nil || tree.Language() == nil {
		return nil
	}
	lang := tree.Language()
	source := tree.Source()
	var refs []ImportRef
	walkTree(tree.RootNode(), lang, func(n *Node) bool {
		switch lang.Name {
		case "go":
			return extractGoImportNode(n, lang, source, &refs)
		case "java":
			return extractJavaImportNode(n, lang, source, &refs)
		case "python":
			return extractPythonImportNode(n, lang, source, &refs)
		case "starlark":
			return extractStarlarkImportNode(n, lang, source, &refs)
		default:
			return true
		}
	})
	return refs
}

// ExtractImportsFromSource returns language-neutral dependency declarations
// directly from source text. It is intended for cold dependency-extraction
// workflows that do not need a public syntax tree.
func ExtractImportsFromSource(lang *Language, source []byte) []ImportRef {
	return ExtractImportsFromSourceWithReport(lang, source).Imports
}

// ExtractImportsFromSourceWithReport returns source-only dependency
// declarations and a confidence report for fallback policy.
func ExtractImportsFromSourceWithReport(lang *Language, source []byte) ImportExtractResult {
	if lang == nil {
		return importExtractResult(nil, ImportExtractUnsupportedConstruct, "language_nil", true)
	}
	switch lang.Name {
	case "go":
		return extractGoImportsFromSourceWithReport(lang, source)
	case "java":
		return importExtractResult(extractJavaImportsFromSource(lang, source), ImportExtractOK, "", false)
	case "python":
		return extractPythonImportsFromSourceWithReport(lang, source)
	case "starlark":
		return extractStarlarkImportsFromSourceWithReport(lang, source)
	default:
		return importExtractResult(nil, ImportExtractUnsupportedConstruct, "unsupported_language", true)
	}
}

func importExtractResult(refs []ImportRef, status ImportExtractStatus, reason string, fallback bool) ImportExtractResult {
	if status == "" {
		status = ImportExtractOK
	}
	return ImportExtractResult{
		Imports:             refs,
		Status:              status,
		Reason:              reason,
		FallbackRecommended: fallback,
	}
}

func walkTree(n *Node, lang *Language, visit func(*Node) bool) {
	if n == nil {
		return
	}
	if !visit(n) {
		return
	}
	for i := 0; i < n.ChildCount(); i++ {
		walkTree(n.Child(i), lang, visit)
	}
}

func extractGoImportNode(n *Node, lang *Language, source []byte, refs *[]ImportRef) bool {
	switch n.Type(lang) {
	case "package_clause":
		if name := firstDescendantText(n, lang, source, "package_identifier", "identifier"); name != "" {
			*refs = append(*refs, ImportRef{
				Lang:      lang.Name,
				Kind:      "package",
				Name:      name,
				StartByte: n.StartByte(),
				EndByte:   n.EndByte(),
			})
		}
		return false
	case "import_declaration":
		specs := collectDescendantsByType(n, lang, "import_spec")
		if len(specs) == 0 {
			specs = []*Node{n}
		}
		for _, spec := range specs {
			pathNode := firstDescendantByType(spec, lang, "interpreted_string_literal", "raw_string_literal")
			if pathNode == nil {
				continue
			}
			path := importStringLiteralText(pathNode.Text(source))
			if path == "" {
				continue
			}
			ref := ImportRef{
				Lang:      lang.Name,
				Kind:      "import",
				Path:      path,
				Name:      lastDottedName(path),
				Alias:     goImportAlias(spec, pathNode, lang, source),
				StartByte: spec.StartByte(),
				EndByte:   spec.EndByte(),
			}
			*refs = append(*refs, ref)
		}
		return false
	}
	return true
}

func goImportAlias(spec, pathNode *Node, lang *Language, source []byte) string {
	for i := 0; i < spec.ChildCount(); i++ {
		child := spec.Child(i)
		if child == nil || child == pathNode || child.StartByte() >= pathNode.StartByte() {
			break
		}
		switch child.Type(lang) {
		case "package_identifier", "identifier":
			return child.Text(source)
		}
		text := strings.TrimSpace(child.Text(source))
		if text == "." || text == "_" {
			return text
		}
	}
	return ""
}

func extractGoImportsFromSourceWithReport(lang *Language, source []byte) ImportExtractResult {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "", source, goparser.ImportsOnly)
	if err != nil && file == nil {
		return importExtractResult(nil, ImportExtractScannerError, "go_parse_imports", true)
	}
	var refs []ImportRef
	if file.Name != nil {
		refs = append(refs, ImportRef{
			Lang:      lang.Name,
			Kind:      "package",
			Name:      file.Name.Name,
			StartByte: tokenOffset(fset, file.Name.Pos()),
			EndByte:   tokenOffset(fset, file.Name.End()),
		})
	}
	for _, spec := range file.Imports {
		if spec == nil || spec.Path == nil {
			continue
		}
		path := importStringLiteralText(spec.Path.Value)
		if path == "" {
			continue
		}
		ref := ImportRef{
			Lang:      lang.Name,
			Kind:      "import",
			Path:      path,
			Name:      lastDottedName(path),
			StartByte: tokenOffset(fset, spec.Pos()),
			EndByte:   tokenOffset(fset, spec.End()),
		}
		if spec.Name != nil {
			ref.Alias = spec.Name.Name
		}
		refs = append(refs, ref)
	}
	if err != nil {
		return importExtractResult(refs, ImportExtractScannerError, "go_parse_imports", true)
	}
	return importExtractResult(refs, ImportExtractOK, "", false)
}

func tokenOffset(fset *gotoken.FileSet, pos gotoken.Pos) uint32 {
	if fset == nil || !pos.IsValid() {
		return 0
	}
	offset := fset.PositionFor(pos, false).Offset
	if offset < 0 {
		return 0
	}
	return uint32(offset)
}

func extractJavaImportNode(n *Node, lang *Language, source []byte, refs *[]ImportRef) bool {
	switch n.Type(lang) {
	case "package_declaration":
		text := strings.TrimSpace(n.Text(source))
		text = strings.TrimPrefix(text, "package")
		text = strings.TrimSuffix(strings.TrimSpace(text), ";")
		if text != "" {
			*refs = append(*refs, ImportRef{
				Lang:      lang.Name,
				Kind:      "package",
				Path:      text,
				Name:      lastDottedName(text),
				StartByte: n.StartByte(),
				EndByte:   n.EndByte(),
			})
		}
		return false
	case "import_declaration":
		text := strings.TrimSpace(n.Text(source))
		text = strings.TrimPrefix(text, "import")
		text = strings.TrimSuffix(strings.TrimSpace(text), ";")
		ref := ImportRef{
			Lang:      lang.Name,
			Kind:      "import",
			StartByte: n.StartByte(),
			EndByte:   n.EndByte(),
		}
		if strings.HasPrefix(text, "static ") {
			ref.Static = true
			text = strings.TrimSpace(strings.TrimPrefix(text, "static"))
		}
		if strings.HasSuffix(text, ".*") {
			ref.Wildcard = true
			text = strings.TrimSuffix(text, ".*")
		}
		ref.Path = text
		ref.Name = lastDottedName(text)
		*refs = append(*refs, ref)
		return false
	}
	return true
}

func extractJavaImportsFromSource(lang *Language, source []byte) []ImportRef {
	clean := stripLineAndBlockComments(source)
	var refs []ImportRef
	stmtStart := 0
	for i, b := range clean {
		if b != ';' {
			continue
		}
		raw := string(clean[stmtStart : i+1])
		leading := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		start := stmtStart + leading
		stmt := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(stmt, "package "):
			text := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(stmt, "package")), ";")
			if text != "" {
				refs = append(refs, ImportRef{
					Lang:      lang.Name,
					Kind:      "package",
					Path:      text,
					Name:      lastDottedName(text),
					StartByte: uint32(start),
					EndByte:   uint32(i + 1),
				})
			}
		case strings.HasPrefix(stmt, "import "):
			ref := javaImportRefFromText(lang, stmt, uint32(start), uint32(i+1))
			if ref.Path != "" {
				refs = append(refs, ref)
			}
		}
		stmtStart = i + 1
	}
	return refs
}

func javaImportRefFromText(lang *Language, text string, startByte, endByte uint32) ImportRef {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "import")
	text = strings.TrimSuffix(strings.TrimSpace(text), ";")
	ref := ImportRef{
		Lang:      lang.Name,
		Kind:      "import",
		StartByte: startByte,
		EndByte:   endByte,
	}
	if strings.HasPrefix(text, "static ") {
		ref.Static = true
		text = strings.TrimSpace(strings.TrimPrefix(text, "static"))
	}
	if strings.HasSuffix(text, ".*") {
		ref.Wildcard = true
		text = strings.TrimSuffix(text, ".*")
	}
	ref.Path = text
	ref.Name = lastDottedName(text)
	return ref
}

func extractPythonImportNode(n *Node, lang *Language, source []byte, refs *[]ImportRef) bool {
	switch n.Type(lang) {
	case "import_statement", "import_from_statement", "future_import_statement":
		start := n.StartByte()
		result := extractPythonImportsFromSourceWithReport(lang, []byte(n.Text(source)))
		for _, ref := range result.Imports {
			ref.StartByte += start
			ref.EndByte += start
			*refs = append(*refs, ref)
		}
		return false
	}
	return true
}

func extractPythonImportsFromSourceWithReport(lang *Language, source []byte) ImportExtractResult {
	var refs []ImportRef
	status := ImportExtractOK
	reason := ""
	fallback := false
	offset := 0
	tripleQuote := ""
	for offset <= len(source) {
		next := offset
		for next < len(source) && source[next] != '\n' {
			next++
		}
		line := string(source[offset:next])
		codeLine := pythonCodeLineOutsideStrings(line, &tripleQuote)
		trimmed := strings.TrimSpace(codeLine)
		start := offset + len(codeLine) - len(strings.TrimLeft(codeLine, " \t\r"))
		stmtEnd := next
		advance := next + 1
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
			stmt := codeLine
			for pythonImportStatementContinues(stmt) && advance <= len(source) {
				lineStart := advance
				lineEnd := lineStart
				for lineEnd < len(source) && source[lineEnd] != '\n' {
					lineEnd++
				}
				stmt += "\n" + pythonCodeLineOutsideStrings(string(source[lineStart:lineEnd]), &tripleQuote)
				stmtEnd = lineEnd
				advance = lineEnd + 1
				if lineEnd == len(source) {
					break
				}
			}
			if pythonImportStatementContinues(stmt) {
				status = ImportExtractScannerError
				reason = "malformed_python_import"
				fallback = true
				break
			}
			trimmed = strings.TrimSpace(stmt)
		}
		switch {
		case strings.HasPrefix(trimmed, "import "):
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, "import"))
			for _, part := range splitImportList(body) {
				path, alias := splitImportAlias(part)
				if path == "" {
					continue
				}
				refs = append(refs, ImportRef{
					Lang:      lang.Name,
					Kind:      "import",
					Path:      path,
					Name:      lastDottedName(path),
					Alias:     alias,
					StartByte: uint32(start),
					EndByte:   uint32(stmtEnd),
				})
			}
		case strings.HasPrefix(trimmed, "from "):
			appendPythonFromImportRefs(lang, trimmed, uint32(start), uint32(stmtEnd), &refs)
		}
		if next == len(source) {
			break
		}
		offset = advance
	}
	return importExtractResult(refs, status, reason, fallback)
}

func appendPythonFromImportRefs(lang *Language, text string, startByte, endByte uint32, refs *[]ImportRef) {
	body := strings.TrimSpace(strings.TrimPrefix(text, "from"))
	fromPart, importPart, ok := strings.Cut(body, " import ")
	if !ok {
		return
	}
	relative := countLeadingDots(fromPart)
	from := strings.TrimLeft(fromPart, ".")
	for _, part := range splitImportList(importPart) {
		name, alias := splitImportAlias(part)
		if name == "" {
			continue
		}
		ref := ImportRef{
			Lang:      lang.Name,
			Kind:      "from_import",
			From:      from,
			Name:      name,
			Alias:     alias,
			Relative:  relative,
			StartByte: startByte,
			EndByte:   endByte,
		}
		if name == "*" {
			ref.Wildcard = true
			ref.Path = joinPythonImportPath(from, "")
		} else {
			ref.Path = joinPythonImportPath(from, name)
		}
		*refs = append(*refs, ref)
	}
}

func extractStarlarkImportNode(n *Node, lang *Language, source []byte, refs *[]ImportRef) bool {
	if n.Type(lang) != "call" {
		return true
	}
	raw := n.Text(source)
	text := strings.TrimLeft(raw, " \t\r\n")
	open := strings.IndexByte(text, '(')
	if open <= 0 || strings.TrimSpace(text[:open]) != "load" {
		return true
	}
	startAdjust := len(raw) - len(text)
	appendStarlarkLoadRefs(lang, text, n.StartByte()+uint32(startAdjust), n.EndByte(), refs)
	return false
}

func extractStarlarkImportsFromSourceWithReport(lang *Language, source []byte) ImportExtractResult {
	text := string(source)
	var refs []ImportRef
	searchFrom := 0
	for {
		start, open := findStarlarkLoadCall(text, searchFrom)
		if start < 0 {
			break
		}
		close := findClosingParen(text, open)
		if close < 0 {
			return importExtractResult(refs, ImportExtractScannerError, "malformed_starlark_load", true)
		}
		if ok, reason := appendStarlarkLoadRefs(lang, text[start:close+1], uint32(start), uint32(close+1), &refs); !ok {
			return importExtractResult(refs, ImportExtractUnsupportedConstruct, reason, true)
		}
		searchFrom = close + 1
	}
	return importExtractResult(refs, ImportExtractOK, "", false)
}

func findStarlarkLoadCall(text string, from int) (int, int) {
	inQuote := byte(0)
	escaped := false
	inComment := false
	for i := from; i < len(text); i++ {
		c := text[i]
		if inComment {
			if c == '\n' {
				inComment = false
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '#':
			inComment = true
			continue
		case '\'', '"':
			inQuote = c
			continue
		}
		if !strings.HasPrefix(text[i:], "load") {
			continue
		}
		beforeOK := i == 0 || !isIdentByte(text[i-1])
		after := i + len("load")
		for after < len(text) && (text[after] == ' ' || text[after] == '\t' || text[after] == '\r' || text[after] == '\n') {
			after++
		}
		if beforeOK && after < len(text) && text[after] == '(' {
			return i, after
		}
	}
	return -1, -1
}

func appendStarlarkLoadRefs(lang *Language, text string, startByte, endByte uint32, refs *[]ImportRef) (bool, string) {
	open := strings.IndexByte(text, '(')
	close := strings.LastIndexByte(text, ')')
	if open <= 0 || close <= open || strings.TrimSpace(text[:open]) != "load" {
		return false, "malformed_starlark_load"
	}
	args := splitImportList(text[open+1 : close])
	if len(args) == 0 {
		return false, "empty_starlark_load"
	}
	from, ok := importStringLiteralValue(args[0])
	if !ok || from == "" {
		return false, "non_literal_starlark_load_module"
	}
	for _, arg := range args[1:] {
		nameText := arg
		alias := ""
		if left, right, ok := strings.Cut(arg, "="); ok {
			alias = strings.TrimSpace(left)
			nameText = right
		}
		name, ok := importStringLiteralValue(nameText)
		if !ok || name == "" {
			return false, "non_literal_starlark_load_symbol"
		}
		*refs = append(*refs, ImportRef{
			Lang:      lang.Name,
			Kind:      "load",
			Path:      from + ":" + name,
			From:      from,
			Name:      name,
			Alias:     alias,
			StartByte: startByte,
			EndByte:   endByte,
		})
	}
	return true, ""
}

func splitImportList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "()")
	s = strings.ReplaceAll(s, "\\\n", "")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = strings.Join(lines, " ")
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitImportAlias(s string) (path string, alias string) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) >= 3 && fields[len(fields)-2] == "as" {
		return strings.Join(fields[:len(fields)-2], " "), fields[len(fields)-1]
	}
	return strings.Join(fields, " "), ""
}

func countLeadingDots(s string) int {
	count := 0
	for count < len(s) && s[count] == '.' {
		count++
	}
	return count
}

func joinPythonImportPath(from, name string) string {
	if from == "" {
		return name
	}
	if name == "" {
		return from
	}
	return from + "." + name
}

func importStringLiteralText(text string) string {
	value, ok := importStringLiteralValue(text)
	if ok {
		return value
	}
	return strings.Trim(strings.TrimSpace(text), "`\"'")
}

func importStringLiteralValue(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if unquoted, err := strconv.Unquote(text); err == nil {
		return unquoted, true
	}
	if len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		var b strings.Builder
		for s := text[1 : len(text)-1]; len(s) > 0; {
			r, _, tail, err := strconv.UnquoteChar(s, '\'')
			if err != nil {
				return "", false
			}
			b.WriteRune(r)
			s = tail
		}
		return b.String(), true
	}
	return "", false
}

func firstDescendantText(n *Node, lang *Language, source []byte, types ...string) string {
	if child := firstDescendantByType(n, lang, types...); child != nil {
		return child.Text(source)
	}
	return ""
}

func firstDescendantByType(n *Node, lang *Language, types ...string) *Node {
	if n == nil {
		return nil
	}
	typ := n.Type(lang)
	for _, want := range types {
		if typ == want {
			return n
		}
	}
	for i := 0; i < n.ChildCount(); i++ {
		if found := firstDescendantByType(n.Child(i), lang, types...); found != nil {
			return found
		}
	}
	return nil
}

func collectDescendantsByType(n *Node, lang *Language, typ string) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(cur *Node) {
		if cur == nil {
			return
		}
		if cur.Type(lang) == typ {
			out = append(out, cur)
		}
		for i := 0; i < cur.ChildCount(); i++ {
			walk(cur.Child(i))
		}
	}
	walk(n)
	return out
}

func lastDottedName(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, ".*")
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 && idx+1 < len(path) {
		path = path[idx+1:]
	}
	if idx := strings.LastIndex(path, "."); idx >= 0 && idx+1 < len(path) {
		return path[idx+1:]
	}
	return path
}

func stripLineAndBlockComments(source []byte) []byte {
	out := make([]byte, len(source))
	copy(out, source)
	inBlock := false
	for i := 0; i < len(out); i++ {
		if inBlock {
			if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i] = ' '
				out[i+1] = ' '
				i++
				inBlock = false
				continue
			}
			if out[i] != '\n' {
				out[i] = ' '
			}
			continue
		}
		if out[i] == '/' && i+1 < len(out) {
			switch out[i+1] {
			case '/':
				out[i] = ' '
				out[i+1] = ' '
				i += 2
				for i < len(out) && out[i] != '\n' {
					out[i] = ' '
					i++
				}
				i--
			case '*':
				out[i] = ' '
				out[i+1] = ' '
				i++
				inBlock = true
			}
		}
	}
	return out
}

func pythonImportStatementContinues(stmt string) bool {
	trimmed := strings.TrimRight(stmt, " \t\r\n")
	if strings.HasSuffix(trimmed, "\\") {
		return true
	}
	return parenBalanceIgnoringQuotes(stmt) > 0
}

func parenBalanceIgnoringQuotes(text string) int {
	depth := 0
	inQuote := byte(0)
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inQuote = c
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth
}

func pythonCodeLineOutsideStrings(line string, tripleQuote *string) string {
	out := []byte(line)
	for i := 0; i < len(line); {
		if tripleQuote != nil && *tripleQuote != "" {
			end := strings.Index(line[i:], *tripleQuote)
			if end < 0 {
				blankBytes(out, i, len(out))
				return string(out)
			}
			end += i + len(*tripleQuote)
			blankBytes(out, i, end)
			i = end
			*tripleQuote = ""
			continue
		}
		if line[i] == '#' {
			blankBytes(out, i, len(out))
			break
		}
		if quote, ok := pythonTripleQuoteAt(line, i); ok {
			end := i + len(quote)
			blankBytes(out, i, end)
			i = end
			if tripleQuote != nil {
				*tripleQuote = quote
			}
			continue
		}
		if line[i] == '\'' || line[i] == '"' {
			i = blankPythonQuotedLineSegment(out, line, i)
			continue
		}
		i++
	}
	return string(out)
}

func pythonTripleQuoteAt(line string, i int) (string, bool) {
	if strings.HasPrefix(line[i:], `"""`) {
		return `"""`, true
	}
	if strings.HasPrefix(line[i:], `'''`) {
		return `'''`, true
	}
	return "", false
}

func blankPythonQuotedLineSegment(out []byte, line string, i int) int {
	quote := line[i]
	out[i] = ' '
	i++
	escaped := false
	for i < len(line) {
		out[i] = ' '
		if escaped {
			escaped = false
			i++
			continue
		}
		if line[i] == '\\' {
			escaped = true
			i++
			continue
		}
		if line[i] == quote {
			i++
			break
		}
		i++
	}
	return i
}

func blankBytes(out []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(out) {
		end = len(out)
	}
	for i := start; i < end; i++ {
		out[i] = ' '
	}
}

func findClosingParen(text string, open int) int {
	depth := 0
	inQuote := byte(0)
	escaped := false
	for i := open; i < len(text); i++ {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inQuote = c
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
