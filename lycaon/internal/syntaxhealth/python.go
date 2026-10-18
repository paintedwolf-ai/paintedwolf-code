package syntaxhealth

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// Grammar recovery can hide invalid Python string and statement structure.
func pythonStructuralFaults(root *gotreesitter.Node, language *gotreesitter.Language, src []byte) []fault {
	var out []fault
	gotreesitter.Walk(root, func(node *gotreesitter.Node, _ int) gotreesitter.WalkAction {
		if node == nil {
			return gotreesitter.WalkContinue
		}
		typ := node.Type(language)
		if typ == "string" {
			out = append(out, pythonStringLineFaults(node, language, src)...)
		}
		if typ == "string_start" && node.Parent() != nil && node.Parent().Type(language) != "string" {
			out = append(out, fault{node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticMissing})
			return gotreesitter.WalkContinue
		}
		if node.ChildCount() != 0 || typ != "try" || string(node.Text(src)) != "try" {
			return gotreesitter.WalkContinue
		}
		statement := node.Parent()
		for statement != nil && statement.Type(language) != "try_statement" {
			statement = statement.Parent()
		}
		if statement == nil || !pythonTryHasHandler(statement, language) {
			out = append(out, fault{
				node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError,
			})
		}
		return gotreesitter.WalkContinue
	})
	return out
}

func pythonTryHasHandler(statement *gotreesitter.Node, language *gotreesitter.Language) bool {
	for i := 0; i < statement.NamedChildCount(); i++ {
		child := statement.NamedChild(i)
		if child == nil {
			continue
		}
		switch child.Type(language) {
		case "except_clause", "finally_clause":
			return true
		}
	}
	return false
}

func pythonStringLineFaults(node *gotreesitter.Node, language *gotreesitter.Language, src []byte) []fault {
	if node.ChildCount() == 0 {
		return nil
	}
	opening := string(node.Child(0).Text(src))
	if strings.HasSuffix(opening, "\"\"\"") || strings.HasSuffix(opening, "'''") {
		return nil
	}
	start := int(node.Child(0).EndByte())
	for i := 1; i < node.ChildCount(); i++ {
		child := node.Child(i)
		// Replacement expressions may span lines. Literal gaps may be absent
		// from the tree, so validate source spans between those expressions.
		if child.Type(language) != "interpolation" {
			continue
		}
		if pythonUnescapedLineBreak(string(src[start:child.StartByte()])) {
			return []fault{{node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError}}
		}
		start = int(child.EndByte())
	}
	if pythonUnescapedLineBreak(string(src[start:node.EndByte()])) {
		return []fault{{node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError}}
	}
	return nil
}

func pythonUnescapedLineBreak(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			if i+1 < len(text) && text[i] == '\r' && text[i+1] == '\n' {
				i++
			}
		} else if text[i] == '\n' || text[i] == '\r' {
			return true
		}
	}
	return false
}
