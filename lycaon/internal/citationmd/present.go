package citationmd

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Rendition is how one occurrence presents. Keep leaves its bytes as written;
// otherwise Text replaces it, and an empty Text removes it.
type Rendition struct {
	Keep bool
	Text string
}

// Render presents a document by span in one linear pass: code byte for byte, text
// as written, each protocol node as decide says, damaged nodes never. It never
// re-emits a removed node's bytes, and raw HTML anchors come out escaped, so the only
// live links in the result are link forms decide kept. A document past a budget
// displays as one literal block.
func Render(doc Document, decide func(Occurrence) Rendition) string {
	text, _ := renderCounted(doc, decide)
	return text
}

// partPlan is one part's decided presentation.
type partPlan struct {
	removed bool
	text    string
	keep    bool
}

func planParts(doc Document, decide func(Occurrence) Rendition) []partPlan {
	plans := make([]partPlan, len(doc.parts))
	kept := map[string]bool{}
	for i, p := range doc.parts {
		switch p.kind {
		case partDamaged:
			plans[i].removed = true
		case partNode:
			var kept []string
			for _, member := range doc.Occurrences[p.first : p.first+p.count] {
				text := decide(member).Text
				if text == "" && member.InlineCode {
					text = doc.Source[member.Span.Start:member.Span.End]
				}
				if text != "" {
					kept = append(kept, text)
				}
			}
			plans[i].text = strings.Join(kept, ", ")
			plans[i].removed = len(kept) == 0
		case partLink:
			switch {
			case p.broken:
				plans[i].text = PlainLabel(*p.link)
			case p.count == 0 && linkGoesNowhere(p.link.Destination):
				plans[i].text = PlainLabel(*p.link)
			case p.count == 0:
				plans[i].keep = true
			default:
				rendition := decide(doc.Occurrences[p.first])
				plans[i].keep, plans[i].text = rendition.Keep, rendition.Text
				kept[p.link.Destination] = kept[p.link.Destination] || rendition.Keep
			}
			plans[i].removed = !plans[i].keep && plans[i].text == ""
		case partLiteral, partRawHTML, partHTMLBlock:
		}
	}
	// A definition renders nothing and can make a later `[label]` live, so a web
	// destination that no kept link vouches for does not stay.
	for i, p := range doc.parts {
		if isWebDestination(p.definition) && !kept[p.definition] {
			plans[i].removed = true
		}
	}
	return plans
}

// runEnds gives, for each removed part, the offset after it and any removed parts
// that follow with only spaces between. Walking backward keeps this linear.
func runEnds(doc Document, plans []partPlan) []int {
	ends := make([]int, len(doc.parts))
	for i := len(doc.parts) - 1; i >= 0; i-- {
		ends[i] = doc.parts[i].span.End
		if i+1 < len(doc.parts) && plans[i].removed && plans[i+1].removed && joinsRemoved(doc.Source[doc.parts[i].span.End:doc.parts[i+1].span.Start]) {
			ends[i] = ends[i+1]
		}
	}
	return ends
}

func isSpaces(text string) bool {
	for i := 0; i < len(text); i++ {
		if !isSpaceByte(text[i]) {
			return false
		}
	}
	return true
}

// renderWalk tracks line facts as the pass moves forward, so that no part rescans
// its line. work counts the bytes those lookups touch.
type renderWalk struct {
	doc      Document
	out      *shaper
	lineHead int
	lineTail int
	runStart int
	inRun    bool
	work     int
	// runRight is where the text after the current removed run resumes, past any
	// separators the run left with nothing to separate.
	runRight int
}

func renderCounted(doc Document, decide func(Occurrence) Rendition) (string, int) {
	if doc.overBudget {
		return escapedDocument(doc.Source), len(doc.Source)
	}
	plans := planParts(doc, decide)
	ends := runEnds(doc, plans)
	var out shaper
	walk := renderWalk{doc: doc, out: &out, lineHead: lineStartIn(doc.Source, doc.Body.Start)}
	at := doc.Body.Start
	for i, p := range doc.parts {
		gap := doc.Source[at:p.span.Start]
		joined := i > 0 && plans[i].removed && plans[i-1].removed && ends[i-1] > doc.parts[i-1].span.End
		// A separator between two removed nodes goes with them.
		if !joined || isSpaces(gap) {
			out.text([]byte(gap))
		}
		walk.moved(at, p.span.Start)
		if !joined {
			walk.inRun = false
		}
		switch {
		case plans[i].removed:
			if !walk.inRun {
				limit := doc.Body.End
				for next := i + 1; next < len(doc.parts) && limit == doc.Body.End; next++ {
					if doc.parts[next].span.Start >= ends[i] {
						limit = doc.parts[next].span.Start
					}
				}
				walk.runStart = p.span.Start
				walk.runRight = walk.tidyAround(ends[i], limit)
			}
			walk.removeNode(p, walk.runRight)
		case p.kind == partLiteral:
			out.protect([]byte(doc.Source[p.span.Start:p.span.End]))
		case p.kind == partRawHTML:
			out.literal([]byte(doc.Source[p.span.Start:p.span.End]))
		case p.kind == partHTMLBlock:
			out.htmlBlock([]byte(doc.Source[p.span.Start:p.span.End]))
		case plans[i].keep:
			writeKeptLink(&out, doc, p)
		case p.kind == partNode:
			out.protect([]byte(plans[i].text))
		default:
			separateFromEscape(&out, plans[i].text)
			out.text([]byte(plans[i].text))
		}
		walk.moved(p.span.Start, p.span.End)
		at = p.span.End
		if plans[i].removed && ends[i] == p.span.End {
			at = max(at, walk.runRight)
		}
	}
	out.text([]byte(doc.Source[at:doc.Body.End]))
	out.close()
	return neutralizeAnchors(out.out, out.protected, out.htmlBlocks), walk.work
}

// moved notes the source just passed, for the line it ended on.
func (w *renderWalk) moved(from, to int) {
	if newline := strings.LastIndexByte(w.doc.Source[from:to], '\n'); newline >= 0 {
		w.lineHead = from + newline + 1
		w.inRun = false
	}
}

// writeKeptLink keeps a link's label open to neutralizing and its destination not.
// `[https://x]` is rewritten, or the closing bracket would render inside its link.
func writeKeptLink(out *shaper, doc Document, p part) {
	source := doc.Source[p.span.Start:p.span.End]
	if p.link == nil {
		out.protect([]byte(source))
		return
	}
	if p.link.Form == LinkAutolink && source[0] != '<' && p.span.End < len(doc.Source) && doc.Source[p.span.End] == ']' {
		rewritten := "<" + p.link.Destination + ">"
		separateFromEscape(out, rewritten)
		out.protect([]byte(rewritten))
		return
	}
	label := p.link.LabelSpan
	if p.link.Form == LinkAutolink {
		out.protect([]byte(source))
		return
	}
	opening := doc.Source[p.span.Start:label.Start]
	out.literal([]byte(opening + labelAutolink.ReplaceAllString(doc.Source[label.Start:label.End], `\<$1`)))
	out.protect([]byte(doc.Source[label.End:p.span.End]))
}

// labelAutolink is an autolink inside a link's label, which the frontend's parser
// would render as a second, unvalidated link.
var labelAutolink = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9+.-]{1,31}:)`)

// separateFromEscape keeps a preceding backslash from escaping a rewritten autolink.
func separateFromEscape(out *shaper, text string) {
	if strings.HasPrefix(text, "<") && !out.pendingSpace() && out.lastByte() == '\\' {
		out.separate()
	}
}

var (
	// emptiedLinePrefix is what is left of a line that only held markup around a
	// removed node.
	emptiedLinePrefix = regexp.MustCompile(`^[ \t>]*(?:(?:[-+*=_#~` + "`" + `]+|[0-9]{1,9}[.)])[ \t]*)*$`)
	// wouldBeThematicBreak is the rest of a line that, joined to markup before it,
	// could read as a thematic break. The escape keeps `**[r]**` literal.
	wouldBeThematicBreak = regexp.MustCompile(`^[-*_][-*_ \t\r\n]*$`)
	// blockStarter is text that would open a block at the start of a line, or make
	// the line a definition.
	blockStarter = regexp.MustCompile(`^[ \t]*(?:[-+*][ \t\r\n]|[-_*=]{2,}[ \t\r\n]*$|[-=*_+]\r?\n?$|#{1,6}(?:[ \t\r\n]|$)|>|` + "```" + `|~~~|[0-9]{1,9}[.)](?:[ \t\r\n]|$)|<[!?]|</?[a-zA-Z][a-zA-Z0-9-]*(?:[ \t\r\n/>]|$)|\[[^\]\n]*\]:)`)
)

// removeNode takes a node out without creating syntax from what surrounds it. A line
// left with only markup goes, leaving a blank line. Text that would open a block
// takes an escape. Neighbours that would otherwise join get one space between them.
func (w *renderWalk) removeNode(p part, runEnd int) {
	out := w.out
	source := w.doc.Source
	first := !w.inRun
	if first {
		w.runStart, w.inRun = p.span.Start, true
	}
	if runEnd >= w.lineTail {
		w.lineTail = lineEnd(source, runEnd)
		w.work += w.lineTail - runEnd
	}
	rest := source[runEnd:max(min(w.lineTail, w.doc.Body.End), runEnd)]
	blank := isBlankHead(rest, &w.work)
	right := byte('\n')
	if rest != "" {
		right = rest[0]
	}
	switch {
	case !out.visible:
		out.remove(true)
		if first && !blank && blockStarter.MatchString(rest) {
			out.escapeNext = true
		}
	case blank && out.onlyTextOnLine() && w.prefixEmptied():
		out.retractLine()
	case out.pendingSpace():
		out.remove(closesSentence(right, rest) || closesEmphasis(rest))
	case wouldBeThematicBreak.MatchString(rest) && w.prefixEmptied():
		out.remove(false)
		out.escapeNext = true
	case closerOf(out.leftNeighbour()) != 0:
		out.remove(false)
	case isSpaceOrNewline(right) || (closesSentence(right, rest) && joinsAfter(out.lastByte())):
		out.remove(false)
	default:
		out.remove(false)
		out.separate()
	}
}

func (w *renderWalk) prefixEmptied() bool {
	prefix := w.doc.Source[w.lineHead:w.runStart]
	w.work += len(prefix)
	return emptiedLinePrefix.MatchString(prefix)
}

// isBlankHead reports whether a line's remainder is only whitespace, stopping at the
// first byte that is not.
func isBlankHead(rest string, work *int) bool {
	for i := 0; i < len(rest); i++ {
		*work++
		if !isSpaceOrNewline(rest[i]) {
			return false
		}
	}
	return true
}

func lineStartIn(text string, at int) int {
	return strings.LastIndexByte(text[:at], '\n') + 1
}

// closesSentence reports a right neighbour that may close up against the word
// before: whitespace, or sentence punctuation that itself ends there.
func closesSentence(right byte, rest string) bool {
	if isSpaceOrNewline(right) {
		return true
	}
	if strings.IndexByte(`.,;:!?)'"`, right) < 0 {
		return false
	}
	return len(rest) < 2 || isSpaceOrNewline(rest[1]) || strings.IndexByte(`.,;:!?)'"`, rest[1]) >= 0
}

// closesEmphasis reports a delimiter run ending there, as in `**x [r]**`, which the
// space before it would stop from closing.
func closesEmphasis(rest string) bool {
	end := 0
	for end < len(rest) && strings.IndexByte("*_~", rest[end]) >= 0 {
		end++
	}
	return end > 0 && (end == len(rest) || isSpaceOrNewline(rest[end]) || strings.IndexByte(`.,;:!?)'"`, rest[end]) >= 0)
}

// joinsAfter rejects a left neighbour that could read a following colon or bracket as
// new syntax, such as `[a]:`.
func joinsAfter(left byte) bool { return left != ']' && left != '<' && left != '[' }

func isSpaceOrNewline(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// Project is the structural presentation that needs no evidence: explicit paths
// become file links, standard links stay, and the rest of the protocol is
// removed.
func Project(doc Document) string { return Render(doc, projectOccurrence) }

func projectOccurrence(o Occurrence) Rendition {
	if o.Link != nil {
		return Rendition{Keep: true}
	}
	if o.Kind == KindCitation && o.Citation != nil && o.Citation.Path != "" {
		return Rendition{Text: FileLink(o.Citation.Path, o.Citation.Line, o.Citation.EndLine)}
	}
	return Rendition{}
}

// FileLink formats a workspace reference as a navigable Markdown link:
// label `path:start-end`, destination `path#Lstart-Lend`.
func FileLink(path string, line, endLine int) string {
	label, fragment := path, ""
	if line > 0 {
		label += ":" + strconv.Itoa(line)
		fragment = "#L" + strconv.Itoa(line)
		if endLine > line {
			label += "-" + strconv.Itoa(endLine)
			fragment += "-L" + strconv.Itoa(endLine)
		}
	}
	return "[" + escapeLabel(label) + "](" + fileDestination(path, fragment) + ")"
}

var labelEscaper = strings.NewReplacer(
	`\`, `\\`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`, `*`, `\*`, `_`, `\_`, "`", "\\`", `<`, `\<`, `&`, `\&`, `~`, `\~`,
)

func escapeLabel(label string) string { return labelEscaper.Replace(label) }

// destinationEscaper keeps a path one URL path. Spaces stay raw inside the
// angle form, so the destination still equals the path citations match by.
var destinationEscaper = strings.NewReplacer(`%`, `%25`, `#`, `%23`, `?`, `%3F`, `<`, `%3C`, `>`, `%3E`, `\`, `%5C`)

func fileDestination(path, fragment string) string {
	dest := destinationEscaper.Replace(path)
	// A colon in the first segment would read as a URL scheme.
	if first, _, _ := strings.Cut(dest, "/"); strings.Contains(first, ":") {
		dest = "./" + dest
	}
	dest += fragment
	if strings.ContainsAny(dest, " ()\t") {
		return "<" + dest + ">"
	}
	return dest
}

// unescapePath decodes a workspace link destination to its path.
func unescapePath(dest string) string {
	if decoded, err := url.PathUnescape(dest); err == nil {
		return decoded
	}
	return dest
}

// PlainLabel is an unvalidated link's text with no link semantics: any protocol node
// in it is removed, and a URL in it stays readable but cannot link.
func PlainLabel(link Link) string {
	if link.Form == LinkAutolink {
		return neutralizeURLs(escapeLabel(link.Label))
	}
	label := strings.TrimSpace(Render(Parse(link.Label, Options{}), func(o Occurrence) Rendition {
		if o.Link != nil {
			return Rendition{Text: o.Link.Label}
		}
		return Rendition{}
	}))
	return neutralizeURLs(label)
}
