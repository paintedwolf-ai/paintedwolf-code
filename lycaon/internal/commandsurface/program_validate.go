package commandsurface

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// validate counts upstream syntax nodes, including words and redirects, and
// rejects excluded nodes throughout the unit without evaluating any word.
func (c *programCompiler) validate(file *syntax.File) {
	var depth, count int64
	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil {
			depth--
			return true
		}
		if c.err != nil {
			return false
		}
		count++
		c.nodeCount = count
		c.maximumDepth = max(c.maximumDepth, depth)
		c.limit(n, "ast_nodes", count)
		c.limit(n, "ast_depth", depth)
		depth++
		c.validateNode(n)
		return c.err == nil
	})
}

func (c *programCompiler) validateNode(n syntax.Node) {
	switch v := n.(type) {
	case *syntax.File, *syntax.Block, *syntax.Subshell, *syntax.IfClause, *syntax.WordIter, *syntax.Lit:
	case *syntax.Word:
		c.validateTilde(v)
		if !c.literalContext[v] && !c.testWords[v] && !c.staticProof[v] {
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
	case *syntax.WhileClause:
		if v.Until {
			c.unsupported(n, "while")
		}
	case *syntax.ForClause:
		if v.Select || v.Braces {
			c.unsupported(n, "for NAME in WORDS; do ...; done")
		}
	case *syntax.FuncDecl:
		_, block := v.Body.Cmd.(*syntax.Block)
		if v.RsrvWord || !v.Parens || v.Name == nil || !block || c.nativeName(v.Name.Value) {
			c.unsupported(n, "NAME() { ...; } with a non-reserved name")
		}
	case *syntax.Assign:
		if v.Append || v.Index != nil || v.Array != nil {
			c.unsupported(n, "scalar NAME=value assignments")
		}
		if v.Name != nil && v.Name.Value == "IFS" {
			c.unsupported(n, "fixed space/tab/newline splitting")
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
	case *syntax.ParamExp:
		c.validateParameter(v)
	case *syntax.DeclClause:
		if v.Variant.Value != "export" {
			c.unsupported(n, "export")
		}
	case *syntax.TestClause:
		c.markTestWord(v.X)
	case *syntax.ParenTest:
		c.markTestWord(v.X)
	case *syntax.UnaryTest:
		c.markTestWord(v.X)
		if !strings.Contains(" ! -n -z -e -f -d -L -r -w -x ", " "+v.Op.String()+" ") {
			c.unsupported(n, "a version 1 unary test predicate")
		}
	case *syntax.BinaryTest:
		c.markTestWord(v.X)
		c.markTestWord(v.Y)
		if !strings.Contains(" = == != -eq -ne -lt -le -gt -ge && || =~ ", " "+v.Op.String()+" ") {
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
	if len(call.Args) == 0 {
		return
	}
	name, known := c.literal(call.Args[0])
	if !known {
		return
	}
	switch name {
	case "eval", "trap", "set", "shopt", "declare", "typeset", "local", "readonly", "let", "exec", ".":
		c.unsupported(call.Args[0], "version 1 built-ins; source -- PATH for explicit scripts")
	}
}
func (c *programCompiler) validateParameter(p *syntax.ParamExp) {
	if p.Flags != nil || p.Excl || p.Width || p.IsSet || p.Split != syntax.OptUnset || p.GlobSubst != syntax.OptUnset || p.RcExpand != syntax.OptUnset || p.Param == nil || p.NestedParam != nil || p.Index != nil || len(p.Modifiers) > 0 || p.Slice != nil || p.Repl != nil || p.Names != 0 || p.Exp != nil {
		c.unsupported(p, "$name, ${name}, ${#name} or supported positional/status parameters")
		return
	}
	name := p.Param.Value
	if name == "-" || name == "$" {
		c.unsupported(p, "invocation variables and supported positional/status parameters")
	}
}
func (c *programCompiler) validateRedirect(r *syntax.Redirect) {
	fd := ""
	if r.N != nil {
		fd = r.N.Value
	}
	if fd != "" && fd != "0" && fd != "1" && fd != "2" {
		c.unsupported(r, "descriptors 0, 1 and 2")
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrIn:
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
			c.limit(r, "here_document_bytes", int64(r.Hdoc.End().Offset()-r.Hdoc.Pos().Offset()))
		}
	case syntax.DplOut:
		target, ok := c.literal(r.Word)
		if !ok || !((fd == "2" && target == "1") || ((fd == "" || fd == "1") && target == "2")) {
			c.unsupported(r, "2>&1 or 1>&2")
		}
	default:
		c.unsupported(r, "version 1 redirects and here-documents")
	}
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
		if !c.reserveHeap(256, word) {
			return
		}
		if c.testWords == nil {
			c.testWords = make(map[*syntax.Word]bool)
		}
		c.testWords[word] = true
	}
}
