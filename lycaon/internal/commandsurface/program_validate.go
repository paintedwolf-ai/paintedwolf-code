package commandsurface

import (
	"errors"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/syntax"
)

// Test predicates admitted in version 1 programs, each operator padded with
// spaces so a whole-token search matches.
const (
	unaryTestOperators  = " ! -n -z -e -f -d -L -r -w -x "
	binaryTestOperators = " = == != -eq -ne -lt -le -gt -ge && || =~ "
)

// validate counts upstream syntax nodes, including words and redirects, and
// rejects excluded nodes throughout the unit without evaluating any word.
func (c *programCompiler) validate(file *syntax.File) {
	var depth int64
	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil {
			depth--
			return true
		}
		if c.err != nil {
			return false
		}
		c.nodeCount++
		c.maximumDepth = max(c.maximumDepth, depth)
		c.limit(n, "ast_nodes", c.nodeCount)
		c.limit(n, "ast_depth", depth)
		depth++
		c.validateNode(n)
		return c.err == nil
	})
}

func (c *programCompiler) validateNode(n syntax.Node) {
	switch v := n.(type) {
	case *syntax.File, *syntax.Block, *syntax.Subshell, *syntax.IfClause, *syntax.WhileClause, *syntax.TimeClause, *syntax.Lit, *syntax.ArrayExpr, *syntax.ParenArithm, *syntax.UnaryArithm, *syntax.BinaryArithm:
	case *syntax.WordIter:
		c.markBraces(true, v.Items...)
	case *syntax.ArrayElem:
		if v.Index != nil {
			c.unsupported(n, "unindexed array elements")
		}
		c.markBraces(true, v.Value)
	case *syntax.Word:
		c.validateTilde(v)
		switch {
		case c.braces.fields[v]:
			c.splitBraces(v)
		case c.literalContext[v] || c.braces.literal[v] || c.staticProof[v]:
		default:
			expanded, err := syntax.ClassifyBraces(v, c.pool)
			if err != nil {
				c.err = parserDiagnostic(err, len(c.source))
			} else if expanded {
				c.unsupported(n, "words without brace expansion")
			}
		}
	case *syntax.Stmt:
		if v.Coprocess || v.Disown {
			c.unsupported(n, "ordinary statements or trailing &")
		}
	case *syntax.CallExpr:
		c.validateCall(v)
	case *syntax.BinaryCmd:
		if v.Op != syntax.Pipe && v.Op != syntax.AndStmt && v.Op != syntax.OrStmt {
			c.unsupported(n, "|, && or ||")
		}
		if v.Op == syntax.Pipe {
			c.limit(n, "pipeline_stages", int64(c.pipelineWidth(v)))
		}
	case *syntax.ForClause:
		if v.Select || v.Braces {
			c.unsupported(n, "for NAME in WORDS; do ...; done")
		}
	case *syntax.CaseClause:
		c.markBraces(false, v.Word)
		for _, item := range v.Items {
			if item.Op != syntax.Break {
				c.unsupported(item, "case items ending with ;;")
			}
			for _, pattern := range item.Patterns {
				c.markBraces(false, pattern)
			}
		}
	case *syntax.CaseItem:
	case *syntax.FuncDecl:
		_, block := v.Body.Cmd.(*syntax.Block)
		if v.RsrvWord || !v.Parens || v.Name == nil || !block || c.nativeName(v.Name.Value) {
			c.unsupported(n, "NAME() { ...; } with a non-reserved name")
		}
	case *syntax.Assign:
		if v.Name != nil && v.Name.Value == "PIPESTATUS" {
			c.unsupported(n, "read-only PIPESTATUS")
		}
		if v.Index != nil {
			if subscriptAllSymbol(v.Index) != "" {
				c.unsupported(n, "numeric array subscript")
			}
			c.validateArithmeticExpr(v.Index, false)
		}
	case *syntax.Redirect:
		c.validateRedirect(v)
	case *syntax.SglQuoted:
		if v.Dollar {
			c.unsupported(n, "ordinary single quotes")
		}
	case *syntax.DblQuoted:
		if v.Dollar {
			c.unsupported(n, "ordinary double quotes")
		}
	case *syntax.CmdSubst:
		if v.TempFile || v.ReplyVar {
			c.unsupported(n, "$() or bounded backticks")
		}
	case *syntax.ProcSubst:
		if v.Op != syntax.CmdIn && v.Op != syntax.CmdOut {
			c.unsupported(n, "<(list) or >(list)")
		}
	case *syntax.ParamExp:
		c.validateParameter(v)
	case *syntax.ArithmExp, *syntax.ArithmCmd:
		c.validateArithmetic(v)
	case *syntax.DeclClause:
		if v.Variant.Value != "export" {
			c.unsupported(n, "export")
		}
		for _, a := range v.Args {
			if a.Append || a.Index != nil || a.Array != nil {
				c.unsupported(a, "scalar export assignments")
			}
		}
	case *syntax.TestClause:
		c.markTestWord(v.X)
	case *syntax.ParenTest:
		c.markTestWord(v.X)
	case *syntax.UnaryTest:
		c.markTestWord(v.X)
		if !strings.Contains(unaryTestOperators, " "+v.Op.String()+" ") {
			c.unsupported(n, "a version 1 unary test predicate")
		}
	case *syntax.BinaryTest:
		c.markTestWord(v.X)
		c.markTestWord(v.Y)
		if !strings.Contains(binaryTestOperators, " "+v.Op.String()+" ") {
			c.unsupported(n, "a version 1 binary test predicate")
		}
	default:
		c.unsupported(n, "a supported version 1 syntax form")
	}
}

// pipelineWidth stops as soon as one too many stages is proven. The pending
// traversal and its precharged slice cannot grow with an unvalidated pipe tree.
func (c *programCompiler) pipelineWidth(n syntax.Node) int {
	maximum := int(languageLimit(c.catalog, "pipeline_stages"))
	if !c.reserveHeap(int64(maximum+1)*16, n) {
		return maximum + 1
	}
	pending := make([]syntax.Node, 1, maximum+1)
	pending[0] = n
	width := 1
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		switch v := current.(type) {
		case *syntax.Stmt:
			pending = append(pending, v.Cmd)
		case *syntax.BinaryCmd:
			if v.Op == syntax.Pipe {
				width++
				if width > maximum {
					return width
				}
				pending = append(pending, v.X, v.Y)
			}
		}
	}
	return width
}
func (c *programCompiler) validateCall(call *syntax.CallExpr) {
	for _, a := range call.Assigns {
		c.markBraces(false, a.Value)
	}
	c.markBraces(true, call.Args...)
	if len(call.Args) == 0 {
		return
	}
	for _, a := range call.Assigns {
		if a.Append || a.Index != nil || a.Array != nil {
			c.unsupported(a, "scalar prefix assignments")
			return
		}
	}
	name, known := c.literal(call.Args[0])
	if !known {
		return
	}
	switch name {
	case "set":
		c.validateSet(call)
	case "trap":
		c.validateTrap(call)
	case "shopt", "declare", "typeset", "local", "readonly", "let", "exec", ".":
		c.unsupported(call.Args[0], "version 1 built-ins; source -- PATH for explicit scripts")
	}
}

func (c *programCompiler) validateTrap(call *syntax.CallExpr) {
	const expected = "trap '...' EXIT"
	args := call.Args[1:]
	if len(args) > 0 {
		if first, ok := c.literal(args[0]); ok && first == "--" {
			args = args[1:]
		}
	}
	if len(args) != 2 {
		c.unsupported(call.Args[0], expected)
		return
	}
	handler, okHandler := c.literal(args[0])
	signal, okSignal := c.literal(args[1])
	if !okHandler || !okSignal || (signal != "EXIT" && signal != "0") {
		c.unsupported(call.Args[0], expected)
		return
	}
	span := c.span(args[0])
	if !c.reserveHeap(int64(len(handler))+512, args[0]) {
		return
	}
	if c.trapSpans == nil {
		c.trapSpans = make(map[*syntax.CallExpr]toolcontract.SourceSpan)
	}
	c.trapSpans[call] = span
	if handler == "" || handler == "-" {
		return
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.WithBudget(syntax.Budget{Pool: c.pool})).Parse(strings.NewReader(handler), "trap")
	if err != nil {
		diag := parserDiagnostic(err, len(handler))
		var sd *SyntaxDiagnostic
		if errors.As(diag, &sd) {
			sd.Span = span
		}
		c.err = diag
		return
	}
	prev := c.spanOverride
	c.spanOverride = &span
	c.validate(file)
	c.spanOverride = prev
	if c.err != nil {
		return
	}
	if c.traps == nil {
		c.traps = make(map[*syntax.CallExpr]*syntax.File)
	}
	c.traps[call] = file
}

// validateSet admits literal -e, -u, -o errexit, -o nounset, -o pipefail and
// their + forms (except +o pipefail), combined as in set -euo pipefail.
func (c *programCompiler) validateSet(call *syntax.CallExpr) {
	const expected = "set with literal -e, -u or -o errexit/nounset/pipefail options"
	args := call.Args[1:]
	if len(args) == 0 {
		c.unsupported(call.Args[0], expected)
	}
	for i := 0; i < len(args); i++ {
		value, ok := c.literal(args[i])
		flags, enable := strings.CutPrefix(value, "-")
		if !enable {
			flags, _ = strings.CutPrefix(value, "+")
		}
		if !ok || flags == "" || flags == value {
			c.unsupported(call.Args[0], expected)
			return
		}
		for _, flag := range flags {
			switch flag {
			case 'e', 'u':
			case 'o':
				if i+1 >= len(args) {
					return
				}
				i++
				name, ok := c.literal(args[i])
				if !ok || (name != "errexit" && name != "nounset" && (name != "pipefail" || !enable)) {
					c.unsupported(call.Args[0], expected)
					return
				}
			default:
				c.unsupported(call.Args[0], expected)
				return
			}
		}
	}
}

func (c *programCompiler) validateParameter(p *syntax.ParamExp) {
	if p.Flags != nil || p.Excl || p.Width || p.IsSet || p.Split != syntax.OptUnset || p.GlobSubst != syntax.OptUnset || p.RcExpand != syntax.OptUnset || p.Param == nil || p.NestedParam != nil || len(p.Modifiers) > 0 || p.Names != 0 || (p.Length && (p.Exp != nil || p.Repl != nil || p.Slice != nil)) {
		c.unsupported(p, parameterForms)
		return
	}
	if p.Index != nil {
		if !validIndexedVariable(p.Param.Value) {
			c.unsupported(p, parameterForms)
			return
		}
		if subscriptAllSymbol(p.Index) == "" {
			c.validateArithmeticExpr(p.Index, c.arithmeticIndexes[p])
		}
	}
	if p.Slice != nil {
		if p.Exp != nil || p.Repl != nil {
			c.unsupported(p, parameterForms)
			return
		}
		c.validateArithmeticExpr(p.Slice.Offset, false)
		c.validateArithmeticExpr(p.Slice.Length, false)
	}
	if p.Exp != nil && !supportedParameterOperators[p.Exp.Op.String()] {
		c.unsupported(p, parameterForms)
		return
	}
	if p.Exp != nil && !parameterNeedsWord(p.Exp.Op.String()) && p.Exp.Word != nil {
		c.unsupported(p, "case conversion without a pattern")
		return
	}
	if p.Exp != nil {
		c.markBraces(false, p.Exp.Word)
		if op := p.Exp.Op.String(); (op == "=" || op == ":=") && !assignableParameter(p) {
			c.unsupported(p, "${name:=word} or ${name=word} assigning a variable or array element")
			return
		}
	}
	if p.Repl != nil {
		c.markBraces(false, p.Repl.Orig, p.Repl.With)
	}
	name := p.Param.Value
	if name == "-" || name == "$" {
		c.unsupported(p, "invocation variables and supported positional/status parameters")
	}
}

func validIndexedVariable(name string) bool {
	return name == "PIPESTATUS" || validWritableVariable(name)
}

const parameterForms = "$name, ${name}, ${#name}, ${name:-word}, ${name:=word}, ${name:+word}, ${name:?word}, ${name#pattern}, ${name%pattern}, ${name/pattern/word}, ${name:offset:length}, ${name^^} or supported array/positional/status parameters"

// assignableParameter reports ${name=word} and ${name:=word} targets: a
// writable variable or one element of an indexed array, never a positional or
// special parameter.
func assignableParameter(p *syntax.ParamExp) bool {
	if p.Index == nil {
		return validWritableVariable(p.Param.Value) || p.Param.Value == "IFS"
	}
	return validWritableVariable(p.Param.Value) && subscriptAllSymbol(p.Index) == ""
}

// supportedParameterOperators select, assign, test or transform a parameter.
var supportedParameterOperators = map[string]bool{
	"-": true, ":-": true, "=": true, ":=": true, "+": true, ":+": true, "?": true, ":?": true,
	"#": true, "##": true, "%": true, "%%": true, "^": true, "^^": true, ",": true, ",,": true,
}

func (c *programCompiler) validateRedirect(r *syntax.Redirect) {
	fd := ""
	if r.N != nil {
		fd = r.N.Value
	}
	if fd != "" && !validRedirectDescriptor(fd) {
		c.unsupported(r, "descriptors 0 through 9")
		return
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrIn:
	case syntax.RdrAll, syntax.AppAll:
		if fd != "" {
			c.unsupported(r, "unnumbered &> or &>>")
		}
	case syntax.Hdoc, syntax.DashHdoc:
		if !c.reserveHeap(512, r) {
			return
		}
		if c.literalContext == nil {
			c.literalContext = make(map[*syntax.Word]bool)
		}
		c.literalContext[r.Word] = true
		c.literalContext[r.Hdoc] = true
		if r.Hdoc != nil && len(r.Hdoc.Parts) > 0 {
			size := uint64(r.Hdoc.End().Offset() - r.Hdoc.Pos().Offset())
			if size > math.MaxInt64 {
				size = math.MaxInt64
			}
			c.limit(r, "here_document_bytes", int64(size))
		}
	case syntax.WordHdoc:
		c.markBraces(false, r.Word)
	case syntax.DplOut, syntax.DplIn:
		target, ok := c.literal(r.Word)
		if !ok || !validRedirectDescriptor(target) {
			c.unsupported(r, "descriptor duplication N>&M or N<&M for descriptors 0 through 9")
		}
	default:
		c.unsupported(r, "version 1 redirects and here-documents")
	}
}

func validRedirectDescriptor(fd string) bool {
	return len(fd) == 1 && fd[0] >= '0' && fd[0] <= '9'
}

func (c *programCompiler) validateTilde(w *syntax.Word) {
	if c.literalContext[w] || len(w.Parts) == 0 {
		return
	}
	literal, ok := w.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(literal.Value, "~") {
		return
	}
	name, _, _ := strings.Cut(literal.Value[1:], "/")
	if name != "" {
		c.unsupported(w, "quoted literal paths or invocation-local variables; no named-user lookup")
	}
}

// Test words do not undergo shell brace expansion. Substitutions within a test
// retain ordinary validation for their own command words.
func (c *programCompiler) markTestWord(expr syntax.TestExpr) {
	if word, ok := expr.(*syntax.Word); ok {
		c.markBraces(false, word)
	}
}
