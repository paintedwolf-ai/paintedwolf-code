package commandsurface

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// CommandIdentity preserves Bash expansion and control semantics. Static argv
// forms also ignore equivalent literal quoting.
func CommandIdentity(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || !utf8.ValidString(line) || int64(len(line)) > SyntaxUnitByteLimit(false) {
		return ""
	}
	tree, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(line), "")
	if err != nil || len(tree.Stmts) == 0 {
		return ""
	}
	static := true
	syntax.Walk(tree, func(node syntax.Node) bool {
		switch n := node.(type) {
		case nil, *syntax.File, *syntax.CallExpr, *syntax.Word, *syntax.BinaryCmd:
		case *syntax.Stmt:
			if n.Negated || n.Background {
				static = false
			}
		case *syntax.SglQuoted:
			if n.Dollar {
				static = false
			}
		case *syntax.DblQuoted:
			if n.Dollar {
				static = false
			}
		case *syntax.Lit:
			if strings.ContainsAny(n.Value, "*?[{~") {
				static = false
			}
		default:
			static = false
		}
		return static
	})
	prefix := "program:"
	if static {
		prefix = "argv:"
		syntax.Walk(tree, func(node syntax.Node) bool {
			if word, ok := node.(*syntax.Word); ok {
				literal, literalErr := expand.Literal(nil, word)
				if literalErr != nil {
					err = literalErr
					return false
				}
				encoded, _ := json.Marshal(literal)
				word.Parts = []syntax.WordPart{&syntax.Lit{Value: string(encoded)}}
			}
			return true
		})
		if err != nil {
			return ""
		}
	}
	var formatted bytes.Buffer
	if err := syntax.NewPrinter().Print(&formatted, tree); err != nil {
		return ""
	}
	return prefix + formatted.String()
}

// SameCommandLine compares valid program identities without evaluating them.
func SameCommandLine(left, right string) bool {
	identity := CommandIdentity(left)
	return identity != "" && identity == CommandIdentity(right)
}

// CheckKey keeps check programs and working directories separate.
func CheckKey(command, cwd string) string {
	identity := CommandIdentity(command)
	if identity == "" {
		identity = "rendered:" + command
	}
	return identity + "\x00" + cwd
}
