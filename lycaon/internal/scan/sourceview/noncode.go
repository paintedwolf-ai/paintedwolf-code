package sourceview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// NonCode identifies comment and literal spans from complete parses.
type NonCode struct {
	source  []byte
	nonCode []bool
}

func ReadNonCode(ctx context.Context, project, path string) (*NonCode, error) {
	absolute := path
	if !filepath.IsAbs(path) {
		absolute = filepath.Join(project, path)
	}
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(canonical, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, fmt.Errorf("comment target outside project: %s", path)
	}
	source, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	detected := filekind.Detect(ctx, filekind.DetectReq{Filename: path, HeadSample: source, Mode: filekind.DepthShallow})
	if detected.Grammar == nil {
		return &NonCode{}, nil
	}
	return ParseNonCode(ctx, detected.Grammar.Name, source)
}

func ParseNonCode(ctx context.Context, language string, source []byte) (*NonCode, error) {
	entry := grammars.DetectLanguageByName(language)
	if entry == nil || entry.Language == nil {
		return nil, fmt.Errorf("no source parser for %s", language)
	}
	grammar := entry.Language()
	tree, err := tsparse.Parse(ctx, grammar, source, tsparse.Analysis)
	if err != nil {
		return nil, &Limitation{Construct: "source_context", Detail: err.Error()}
	}
	if tree == nil {
		return nil, &Limitation{Construct: "source_context", Detail: "Source context parsing produced no tree for " + language}
	}
	defer tree.Release()
	if tree.RootNode() == nil || tree.RootNode().HasError() || hasSourceRecovery(tree.RootNode()) {
		return nil, &Limitation{Construct: "source_context", Detail: "Source context parsing is incomplete for " + language}
	}
	result := &NonCode{source: source, nonCode: make([]bool, len(source))}
	mark := func(node *gotreesitter.Node, value bool) {
		for offset := int(node.StartByte()); offset < int(node.EndByte()); offset++ {
			result.nonCode[offset] = value
		}
	}
	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node == nil {
			return
		}
		switch node.Type(grammar) {
		case "comment", "line_comment", "block_comment", "pod":
			mark(node, true)
			return
		}
		switch node.Type(grammar) {
		case "string", "string_literal", "raw_string_literal", "interpreted_string_literal", "verbatim_string_characters", "expandable_string_literal", "expandable_here_string_literal", "single_quoted_string", "double_quoted_string":
			mark(node, true)
		case "interpolation", "interpolated_expression", "sub_expression", "string_interpolation":
			mark(node, false)
		}
		for i := 0; i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
	}
	walk(tree.RootNode())
	return result, nil
}

func hasSourceRecovery(node *gotreesitter.Node) bool {
	if node.IsError() || node.IsMissing() {
		return true
	}
	for i := 0; i < node.ChildCount(); i++ {
		if hasSourceRecovery(node.Child(i)) {
			return true
		}
	}
	return false
}

// Contains uses one-based line and byte-column coordinates.
func (c *NonCode) Contains(line, column int) bool {
	offset, ok := c.offset(line, column)
	return ok && c.nonCodeAt(offset)
}

// CoversSpan suppresses only a complete match contained in non-executable text.
// Missing or invalid end coordinates cannot establish that fact.
func (c *NonCode) CoversSpan(line, column, endLine, endColumn int) bool {
	start, ok := c.offset(line, column)
	if !ok {
		return false
	}
	end, ok := c.offset(endLine, endColumn)
	if !ok || end <= start {
		return false
	}
	for offset := start; offset < end; offset++ {
		if !c.nonCodeAt(offset) {
			return false
		}
	}
	return true
}

func (c *NonCode) nonCodeAt(offset int) bool {
	return offset >= 0 && offset < len(c.nonCode) && c.nonCode[offset]
}

func (c *NonCode) offset(line, column int) (int, bool) {
	if line < 1 || column < 1 {
		return 0, false
	}
	offset, current := 0, 1
	for offset < len(c.source) && current < line {
		if c.source[offset] == '\n' {
			current++
		}
		offset++
	}
	if current != line {
		return 0, false
	}
	target := offset + column - 1
	if target > len(c.source) {
		return 0, false
	}
	for i := offset; i < target; i++ {
		if c.source[i] == '\n' {
			return 0, false
		}
	}
	return target, true
}
