package commandsurface

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
	"mvdan.cc/sh/v3/syntax"
)

// CommandIdentity preserves Bash expansion and control semantics. Static argv
// forms also ignore equivalent literal quoting.
func CommandIdentity(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || int64(len(line)) > SyntaxUnitByteLimit(false) {
		return ""
	}
	tree, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(line), "")
	if err != nil {
		return ""
	}
	static := true
	syntax.Walk(tree, func(node syntax.Node) bool {
		switch n := node.(type) {
		case nil, *syntax.File, *syntax.Stmt, *syntax.CallExpr, *syntax.Word, *syntax.SglQuoted, *syntax.DblQuoted, *syntax.BinaryCmd:
		case *syntax.Lit:
			if strings.ContainsAny(n.Value, "*?[{~") {
				static = false
			}
		default:
			static = false
		}
		return static
	})
	if static {
		if stages, err := exec.StagesFromCommandLine(line); err == nil {
			raw, err := json.Marshal(stages)
			if err == nil {
				return "argv:" + string(raw)
			}
		}
	}
	var formatted bytes.Buffer
	if err := syntax.NewPrinter().Print(&formatted, tree); err != nil {
		return ""
	}
	return "program:" + formatted.String()
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
