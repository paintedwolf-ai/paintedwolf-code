package citationmd

import (
	"bytes"
	"sort"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// inlineExtent is the exact source range of one inline node. labelEnd is the
// closing bracket of a link label.
type inlineExtent struct {
	start    int
	end      int
	labelEnd int
}

// parseLog is what one parse records beside the AST: where inline nodes ended,
// a fact goldmark does not keep, and the work its link parser has done.
type parseLog struct {
	extents  map[ast.Node]inlineExtent
	linkWork int
}

var parseLogKey = parser.NewContextKey()

// extentRecorder wraps an inline parser to capture where its node ended. A link
// attempt rescans the rest of its line, so attempts are charged against
// maxStructureWork and refused once it is spent; the parse is then discarded.
type extentRecorder struct {
	parser.InlineParser
}

func (r extentRecorder) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	log, _ := pc.Get(parseLogKey).(*parseLog)
	line, entry := block.PeekLine()
	if len(line) > 1 && line[0] == ']' && line[1] == '(' {
		if log.linkWork += len(line); log.linkWork > maxStructureWork {
			return nil
		}
	}
	node := r.InlineParser.Parse(parent, block, pc)
	if node == nil {
		return nil
	}
	_, after := block.Position()
	switch node.(type) {
	case *ast.CodeSpan, *ast.RawHTML, *ast.AutoLink:
		log.extents[node] = inlineExtent{start: entry.Start, end: after.Start}
	case *ast.Link, *ast.Image:
		log.extents[node] = inlineExtent{start: node.Pos(), end: after.Start, labelEnd: entry.Start}
	}
	return node
}

// CloseBlock forwards the link parser's end-of-block cleanup.
func (r extentRecorder) CloseBlock(parent ast.Node, block text.Reader, pc parser.Context) {
	if closer, ok := r.InlineParser.(parser.CloseBlocker); ok {
		closer.CloseBlock(parent, block, pc)
	}
}

// markdownParser is the dialect the frontend renders, CommonMark plus GFM, less the
// emphasis and strikethrough parsers, which cannot change a literal zone or a link
// boundary and whose delimiter matching is quadratic in a block's delimiters, and
// less linkify: bare URLs follow the frontend's own rule, in barelink.go.
var markdownParser = sync.OnceValue(func() parser.Parser {
	configured := parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
		parser.WithInlineParsers(
			util.Prioritized(extentRecorder{parser.NewCodeSpanParser()}, 100),
			util.Prioritized(extentRecorder{parser.NewLinkParser()}, 200),
			util.Prioritized(extentRecorder{parser.NewAutoLinkParser()}, 300),
			util.Prioritized(extentRecorder{parser.NewRawHTMLParser()}, 400),
		),
	)
	md := goldmark.New(goldmark.WithParser(configured))
	extension.Table.Extend(md)
	extension.TaskList.Extend(md)
	return md.Parser()
})

// structure is one Markdown parse: its literal zones and the links among them.
// exhausted means the parse spent its budget and its zones must not be trusted.
// wrapper is the body of a fence that is the entire source, when there is one.
type structure struct {
	zones     []Zone
	links     map[int]*Link
	exhausted bool
	wrapper   *Span
	body      Span
	// definitions holds each link definition's destination by its zone's start.
	definitions map[int]string
}

// LiteralZones returns the source's literal zones, under Parse's budgets. Past a
// budget they are the coarse zones of a source that was not structurally parsed.
func LiteralZones(source string) []Zone {
	return Parse(source, Options{}).Zones
}

// documentStructure parses source and, once, the body of a whole-response
// Markdown fence, with spans mapped back to the source.
func documentStructure(source string) structure {
	outer := parseStructure(source)
	if outer.exhausted || outer.wrapper == nil {
		return outer
	}
	body := *outer.wrapper
	inner := parseStructure(source[body.Start:body.End])
	inner.shift(body)
	return inner
}

func (s *structure) shift(body Span) {
	s.body = body
	moved := make(map[int]*Link, len(s.links))
	for start, link := range s.links {
		link.LabelSpan = Span{link.LabelSpan.Start + body.Start, link.LabelSpan.End + body.Start}
		moved[start+body.Start] = link
	}
	s.links = moved
	definitions := make(map[int]string, len(s.definitions))
	for start, destination := range s.definitions {
		definitions[start+body.Start] = destination
	}
	s.definitions = definitions
	for i := range s.zones {
		s.zones[i].Span = Span{s.zones[i].Span.Start + body.Start, s.zones[i].Span.End + body.Start}
	}
}

// TextRanges returns the complement of LiteralZones: the only ranges in which
// custom nodes may begin.
func TextRanges(source string) []Span {
	doc := Parse(source, Options{})
	var out []Span
	at := doc.Body.Start
	for _, zone := range doc.Zones {
		if zone.Span.Start > at {
			out = append(out, Span{at, zone.Span.Start})
		}
		at = max(at, zone.Span.End)
	}
	if at < doc.Body.End {
		out = append(out, Span{at, doc.Body.End})
	}
	return out
}

func parseStructure(source string) structure {
	data := []byte(source)
	log := &parseLog{extents: map[ast.Node]inlineExtent{}}
	pc := parser.NewContext()
	pc.Set(parseLogKey, log)
	doc := markdownParser().Parse(text.NewReader(data), parser.WithContext(pc))
	out := structure{links: map[int]*Link{}, definitions: map[int]string{}, exhausted: log.linkWork > maxStructureWork, body: Span{0, len(data)}, wrapper: wrapperBody(doc, data)}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		return out.visit(n, data, log), nil
	})
	sort.SliceStable(out.zones, func(i, j int) bool { return out.zones[i].Span.Start < out.zones[j].Span.Start })
	return out
}

// wrapperBody finds the body of a Markdown fence that is the entire source. Only
// whitespace may surround it. An indented fence does not qualify: stripping its
// indent would leave a body that is no single span of the source.
func wrapperBody(doc ast.Node, data []byte) *Span {
	fence, ok := doc.FirstChild().(*ast.FencedCodeBlock)
	if !ok || fence.NextSibling() != nil || fence.Info == nil {
		return nil
	}
	info := strings.ToLower(strings.TrimSpace(string(fence.Info.Value(data))))
	lines := fence.Lines()
	if (info != "markdown" && info != "md") || lines.Len() == 0 {
		return nil
	}
	span := fenceSpan(fence, data)
	if isSpaceByte(data[span.Start]) || len(bytes.TrimSpace(data[:span.Start])) > 0 || len(bytes.TrimSpace(data[span.End:])) > 0 {
		return nil
	}
	return &Span{lines.At(0).Start, lines.At(lines.Len() - 1).Stop}
}

func (s *structure) visit(n ast.Node, data []byte, log *parseLog) ast.WalkStatus {
	switch node := n.(type) {
	case *ast.FencedCodeBlock:
		s.addBlock(ZoneFence, fenceSpan(node, data))
	case *ast.CodeBlock:
		s.addBlock(ZoneIndented, linesSpan(node, data))
	case *ast.HTMLBlock:
		span := linesSpan(node, data)
		if node.HasClosure() {
			span.End = node.ClosureLine.Stop
		}
		s.addBlock(ZoneHTMLBlock, span)
	case *ast.LinkReferenceDefinition:
		span := linesSpan(node, data)
		s.addBlock(ZoneDefinition, span)
		s.definitions[span.Start] = cleanDestination(node.Destination)
	case *ast.CodeSpan:
		s.addInline(ZoneCodeSpan, log, n)
	case *ast.RawHTML:
		s.addInline(ZoneRawHTML, log, n)
	case *ast.Image:
		s.addInline(ZoneImage, log, n)
	case *ast.Link:
		s.addLink(node, data, log)
	case *ast.AutoLink:
		s.addAutolink(node, data, log)
	default:
		return ast.WalkContinue
	}
	return ast.WalkSkipChildren
}

func (s *structure) addBlock(kind ZoneKind, span Span) {
	if span.End > span.Start {
		s.zones = append(s.zones, Zone{Kind: kind, Span: span})
	}
}

func (s *structure) addInline(kind ZoneKind, log *parseLog, n ast.Node) {
	if extent, ok := log.extents[n]; ok {
		s.zones = append(s.zones, Zone{Kind: kind, Span: Span{extent.start, extent.end}})
	}
}

func (s *structure) addLink(node *ast.Link, data []byte, log *parseLog) {
	extent, ok := log.extents[node]
	if !ok {
		return
	}
	s.zones = append(s.zones, Zone{Kind: ZoneLink, Span: Span{extent.start, extent.end}})
	form := LinkInline
	if node.Reference != nil {
		form = LinkReference
	}
	label := Span{extent.start + 1, extent.labelEnd}
	s.links[extent.start] = &Link{Form: form, Label: string(data[label.Start:label.End]), LabelSpan: label, Destination: cleanDestination(node.Destination)}
}

func (s *structure) addAutolink(node *ast.AutoLink, data []byte, log *parseLog) {
	extent, ok := log.extents[node]
	if !ok {
		return
	}
	span := Span{extent.start, extent.end}
	s.zones = append(s.zones, Zone{Kind: ZoneAutolink, Span: span})
	if node.AutoLinkType == ast.AutoLinkURL {
		s.links[span.Start] = &Link{Form: LinkAutolink, Label: string(node.Label(data)), LabelSpan: Span{span.Start + 1, span.End - 1}, Destination: string(node.URL(data))}
	}
}

func cleanDestination(raw []byte) string {
	return string(util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(raw))))
}

// linesSpan covers a block's raw lines from the start of its first source line.
func linesSpan(node ast.Node, data []byte) Span {
	lines := node.Lines()
	if lines.Len() == 0 {
		return Span{}
	}
	return Span{lineStart(data, lines.At(0).Start), lines.At(lines.Len() - 1).Stop}
}

// fenceSpan covers the opening fence through its closing line when there is one.
func fenceSpan(node *ast.FencedCodeBlock, data []byte) Span {
	opener := max(node.Pos(), 0)
	start := lineStart(data, opener)
	end := lineEndBytes(data, opener)
	fence, run := fenceOpening(data[opener:end])
	if lines := node.Lines(); lines.Len() > 0 {
		end = lines.At(lines.Len() - 1).Stop
	}
	if end < len(data) {
		next := lineEndBytes(data, end)
		if isFenceCloser(data[end:next], fence, run, containerOf(node)) {
			end = next
		}
	}
	return Span{start, end}
}

func fenceOpening(line []byte) (byte, int) {
	at := 0
	for at < len(line) && line[at] != '`' && line[at] != '~' {
		at++
	}
	if at == len(line) {
		return '`', 3
	}
	end := at
	for end < len(line) && line[end] == line[at] {
		end++
	}
	return line[at], end - at
}

// containerPrefix is one enclosing block a continuation line must re-enter.
type containerPrefix struct {
	quote  bool
	indent int
}

func containerOf(node ast.Node) []containerPrefix {
	var out []containerPrefix
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch block := parent.(type) {
		case *ast.Blockquote:
			out = append([]containerPrefix{{quote: true}}, out...)
		case *ast.ListItem:
			out = append([]containerPrefix{{indent: block.Offset}}, out...)
		}
	}
	return out
}

// isFenceCloser applies CommonMark's closing rule inside the fence's containers:
// the same character, a run at least as long as the opener, up to three spaces of
// indent, and nothing else on the line. A line that leaves a container closes the
// fence without being its closer.
func isFenceCloser(line []byte, fence byte, run int, containers []containerPrefix) bool {
	at := 0
	for _, container := range containers {
		if container.quote {
			skip := 0
			for skip < 3 && at+skip < len(line) && line[at+skip] == ' ' {
				skip++
			}
			if at+skip >= len(line) || line[at+skip] != '>' {
				return false
			}
			at += skip + 1
			if at < len(line) && isSpaceByte(line[at]) {
				at++
			}
			continue
		}
		for skip := 0; skip < container.indent; skip++ {
			if at >= len(line) || !isSpaceByte(line[at]) {
				return false
			}
			at++
		}
	}
	indent := 0
	for at < len(line) && line[at] == ' ' && indent < 4 {
		at++
		indent++
	}
	if indent > 3 {
		return false
	}
	length := 0
	for at < len(line) && line[at] == fence {
		at++
		length++
	}
	return length >= run && isBlankLine(string(line[at:]))
}

func lineStart(data []byte, at int) int {
	for at > 0 && data[at-1] != '\n' {
		at--
	}
	return at
}

func lineEndBytes(data []byte, at int) int {
	if next := bytes.IndexByte(data[at:], '\n'); next >= 0 {
		return at + next + 1
	}
	return len(data)
}

// lineEnd returns the offset just past the newline of the line holding at.
func lineEnd(text string, at int) int {
	if at >= len(text) {
		return len(text)
	}
	if next := strings.IndexByte(text[at:], '\n'); next >= 0 {
		return at + next + 1
	}
	return len(text)
}
