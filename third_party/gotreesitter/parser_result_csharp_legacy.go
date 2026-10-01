package gotreesitter

import (
	"unicode"
	"unicode/utf8"
)

// This file contains conservative compatibility repairs for C# language
// artifacts that do not certify the corresponding native result shapes.
// Exact built-in blobs skip these walks through NativeResultCompatibility.

const csharpLegacyResultCompatibility = ResultCompatibilityCSharpNativeNotNull |
	ResultCompatibilityCSharpNativeUnicodeIdentifiers |
	ResultCompatibilityCSharpNativeScopedLambdaStatements |
	ResultCompatibilityCSharpNativeScopedLambdaBlocks |
	ResultCompatibilityCSharpNativeQueryExpressions

func csharpMissingNativeResultCompatibility(lang *Language) ResultCompatibilityCapability {
	if lang == nil {
		return csharpLegacyResultCompatibility
	}
	return csharpLegacyResultCompatibility &^ lang.NativeResultCompatibility
}

func normalizeCSharpUnicodeIdentifierSpans(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "c_sharp" || len(source) == 0 {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(lang) == "identifier" && len(n.children) == 0 {
			if end := csharpUnicodeIdentifierEnd(source, n.startByte); end > n.endByte && csharpCanExtendLeafNodeTo(n, end) {
				n.endByte = end
				n.endPoint = advancePointByBytes(Point{}, source[:end])
			}
		}
	})
}

func csharpUnicodeIdentifierEnd(source []byte, start uint32) uint32 {
	if int(start) >= len(source) {
		return start
	}
	r, size := utf8.DecodeRune(source[start:])
	if size == 0 || r == utf8.RuneError && size == 1 || !csharpIdentifierStartRune(r) {
		return start
	}
	pos := start + uint32(size)
	for int(pos) < len(source) {
		r, size = utf8.DecodeRune(source[pos:])
		if size == 0 || r == utf8.RuneError && size == 1 || !csharpIdentifierContinueRune(r) {
			break
		}
		pos += uint32(size)
	}
	return pos
}

func csharpIdentifierStartRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.In(r, unicode.Nl)
}

func csharpIdentifierContinueRune(r rune) bool {
	return csharpIdentifierStartRune(r) ||
		unicode.IsDigit(r) ||
		unicode.In(r, unicode.Mn, unicode.Mc, unicode.Pc, unicode.Cf)
}

func csharpCanExtendLeafNodeTo(n *Node, end uint32) bool {
	if n == nil || end <= n.endByte {
		return false
	}
	if n.parent == nil {
		return true
	}
	for _, sibling := range n.parent.children {
		if sibling == nil || sibling == n {
			continue
		}
		if sibling.startByte >= n.endByte && sibling.startByte < end {
			return false
		}
	}
	return true
}

func normalizeCSharpTypeConstraintKeywords(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "c_sharp" {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(lang) == "type_parameter_constraint" && len(n.children) == 1 {
			child := n.children[0]
			if child != nil && child.Type(lang) == "identifier" && len(child.children) == 1 {
				inner := child.children[0]
				if inner != nil && inner.Type(lang) == "notnull" && !inner.isNamed() &&
					child.startByte == inner.startByte && child.endByte == inner.endByte {
					n.children[0] = inner
					inner.parent = n
					inner.childIndex = 0
					if len(n.fieldIDs()) > 0 {
						n.fieldIDs()[0] = 0
					}
					if len(n.fieldSources()) > 0 {
						n.fieldSources()[0] = fieldSourceNone
					}
				}
			}
		}
	})
}
