package repomap

import (
	"bytes"
	"math"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Markdown headings use the same block grammar as rendered documents.
func markdownTags(src []byte) tagParseResult {
	lines := []int{0}
	for i, b := range src {
		if b == '\n' {
			lines = append(lines, i+1)
		}
	}
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(src))
	var out tagParseResult
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := node.(*ast.Heading)
		if !entering || !ok || heading.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		first := heading.Lines().At(0)
		last := heading.Lines().At(heading.Lines().Len() - 1)
		nameRange, valid := markdownSourceRange(lines, len(src), first.Start, last.Stop)
		if !valid {
			out.failure = &tsparse.Failure{Reason: "source_range", Language: "markdown", SourceBytes: len(src), Detail: "heading range lies outside the source"}
			return ast.WalkStop, nil
		}
		name := strings.TrimSpace(string(heading.Lines().Value(src)))
		start := lines[nameRange.StartPoint.Row]
		lastContent := last.Stop
		for lastContent > last.Start && (src[lastContent-1] == '\n' || src[lastContent-1] == '\r') {
			lastContent--
		}
		endRow := sort.Search(len(lines), func(i int) bool { return lines[i] > lastContent }) - 1
		// Setext headings include their underline; AST text segments exclude it.
		if !bytes.Contains(src[start:first.Start], []byte("#")) && endRow+1 < len(lines) {
			endRow++
		}
		end := len(src)
		if endRow+1 < len(lines) {
			end = lines[endRow+1]
		}
		span, valid := markdownSourceRange(lines, len(src), start, end)
		if !valid {
			out.failure = &tsparse.Failure{Reason: "source_range", Language: "markdown", SourceBytes: len(src), Detail: "heading range lies outside the source"}
			return ast.WalkStop, nil
		}
		out.tags = append(out.tags, gotreesitter.Tag{
			Kind: "definition.section", Name: name,
			Range: span, NameRange: nameRange,
		})
		return ast.WalkSkipChildren, nil
	})
	return out
}

func markdownSourceRange(lines []int, sourceSize, start, end int) (gotreesitter.Range, bool) {
	if start < 0 || end < 0 || end < start || end > sourceSize || start > math.MaxUint32 || end > math.MaxUint32 {
		return gotreesitter.Range{}, false
	}
	point := func(offset int) (gotreesitter.Point, bool) {
		row := sort.Search(len(lines), func(i int) bool { return lines[i] > offset }) - 1
		if row < 0 || row > math.MaxUint32 {
			return gotreesitter.Point{}, false
		}
		column := offset - lines[row]
		if column < 0 || column > math.MaxUint32 {
			return gotreesitter.Point{}, false
		}
		return gotreesitter.Point{Row: uint32(row), Column: uint32(column)}, true
	}
	startPoint, startOK := point(start)
	endPoint, endOK := point(end)
	if !startOK || !endOK {
		return gotreesitter.Range{}, false
	}
	return gotreesitter.Range{
		StartByte: uint32(start), EndByte: uint32(end), StartPoint: startPoint, EndPoint: endPoint,
	}, true
}
