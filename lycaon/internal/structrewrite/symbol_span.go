package structrewrite

import (
	"context"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
)

// statementWrapperTypes are CST nodes that wrap one definition with modifiers
// (export, decorators, a single Go var/const). Containers that group other
// definitions — class_body, source_file, block — are not wrappers.
var statementWrapperTypes = map[string]bool{
	"ambient_declaration":  true,
	"const_declaration":    true,
	"decorated_definition": true,
	"export_declaration":   true,
	"export_statement":     true,
	"var_declaration":      true,
}

// expandDefinitionSpans grows each tagged span to its exclusive statement
// wrapper so read and edit share the text a caller would copy from the file.
func expandDefinitionSpans(ctx context.Context, langName, filename string, src []byte, spans []repomap.DefinitionSpan) ([]repomap.DefinitionSpan, error) {
	if len(spans) == 0 {
		return spans, nil
	}
	lang, _, err := resolveLanguage(langName, filename)
	if err != nil {
		return nil, err
	}
	tree, err := tsparse.Parse(ctx, lang, src, tsparse.Analysis)
	if err != nil {
		return nil, err
	}
	defer tree.Release()
	root := tree.RootNode()
	out := make([]repomap.DefinitionSpan, len(spans))
	for i, sp := range spans {
		out[i] = expandOneDefinitionSpan(lang, root, src, sp, spans)
	}
	return out, nil
}

func expandOneDefinitionSpan(lang *gotreesitter.Language, root *gotreesitter.Node, src []byte, sp repomap.DefinitionSpan, all []repomap.DefinitionSpan) repomap.DefinitionSpan {
	startOff, startOK := byteOffset(sp.StartByte)
	endOff, endOK := byteOffset(sp.EndByte)
	if !startOK || !endOK {
		return sp
	}
	n := coveringNode(root, startOff, endOff)
	if n == nil {
		return sp
	}
	for {
		p := n.Parent()
		if p == nil || !statementWrapperTypes[p.Type(lang)] {
			break
		}
		if wrapperHasSiblingDefinition(p, sp, all) {
			break
		}
		n = p
	}
	start, end := int(n.StartByte()), int(n.EndByte())
	if start == sp.StartByte && end == sp.EndByte {
		return sp
	}
	if start < 0 || end > len(src) || start > end {
		return sp
	}
	ps, pe := n.StartPoint(), n.EndPoint()
	sp.StartByte = start
	sp.EndByte = end
	sp.StartRow = int(ps.Row)
	sp.StartCol = int(ps.Column)
	sp.EndRow = int(pe.Row)
	sp.EndCol = int(pe.Column)
	return sp
}

func coveringNode(n *gotreesitter.Node, start, end uint32) *gotreesitter.Node {
	if n == nil || n.StartByte() > start || n.EndByte() < end {
		return nil
	}
	for i := 0; i < n.ChildCount(); i++ {
		if c := coveringNode(n.Child(i), start, end); c != nil {
			return c
		}
	}
	return n
}

func wrapperHasSiblingDefinition(p *gotreesitter.Node, self repomap.DefinitionSpan, all []repomap.DefinitionSpan) bool {
	ps, pe := int(p.StartByte()), int(p.EndByte())
	for _, sp := range all {
		if sameDefinitionSpan(sp, self) {
			continue
		}
		inside := sp.StartByte >= ps && sp.EndByte <= pe
		outsideSelf := sp.StartByte < self.StartByte || sp.EndByte > self.EndByte
		if inside && outsideSelf {
			return true
		}
	}
	return false
}

func sameDefinitionSpan(a, b repomap.DefinitionSpan) bool {
	return a.StartByte == b.StartByte && a.EndByte == b.EndByte && a.Name == b.Name && a.Kind == b.Kind
}
