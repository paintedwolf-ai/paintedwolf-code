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

// Render presents a document by span: literal zones byte for byte, text as
// written, each protocol node as decide says, and damaged nodes never. Removal
// never creates Markdown: see removeNode.
func Render(doc Document, decide func(Occurrence) Rendition) string {
	var out shaper
	at := doc.Body.Start
	for i, p := range doc.parts {
		out.text([]byte(doc.Source[at:p.span.Start]))
		at = p.span.End
		switch p.kind {
		case partLiteral:
			out.literal([]byte(doc.Source[p.span.Start:p.span.End]))
		case partDamaged:
			removeNode(&out, doc, i, decide)
		case partLink:
			renderLink(&out, doc, i, decide)
		case partNode:
			if !renderMembers(&out, doc.Occurrences[p.first:p.first+p.count], decide) {
				removeNode(&out, doc, i, decide)
			}
		}
	}
	out.text([]byte(doc.Source[at:doc.Body.End]))
	out.close()
	return out.String()
}

func renderLink(out *shaper, doc Document, i int, decide func(Occurrence) Rendition) {
	p := doc.parts[i]
	source := []byte(doc.Source[p.span.Start:p.span.End])
	if p.count == 0 {
		out.literal(source)
		return
	}
	o := doc.Occurrences[p.first]
	switch rendition := decide(o); {
	case rendition.Keep:
		if isBracketedBareLink(doc, p, o) {
			source = []byte("<" + o.Link.Destination + ">")
		}
		out.literal(source)
	case rendition.Text != "":
		out.text([]byte(rendition.Text))
	case o.Link.Form == LinkRawHTML:
		out.remove(false)
	default:
		removeNode(out, doc, i, decide)
	}
}

// isBracketedBareLink finds `[https://x]`, which would otherwise render with the
// closing bracket inside its link.
func isBracketedBareLink(doc Document, p part, o Occurrence) bool {
	return o.Link.Form == LinkAutolink && p.span.Start > 0 && p.span.End < len(doc.Source) &&
		doc.Source[p.span.Start-1] == '[' && doc.Source[p.span.End] == ']' && doc.Source[p.span.Start] != '<'
}

// renderMembers joins a group's surviving members and reports whether any did.
func renderMembers(out *shaper, members []Occurrence, decide func(Occurrence) Rendition) bool {
	var kept []string
	for _, member := range members {
		if text := decide(member).Text; text != "" {
			kept = append(kept, text)
		}
	}
	if len(kept) == 0 {
		return false
	}
	out.text([]byte(strings.Join(kept, ", ")))
	return true
}

var (
	// emptiedLinePrefix is what is left of a line that only held container markup
	// around a removed node.
	emptiedLinePrefix = regexp.MustCompile(`^[ \t>]*(?:(?:[-+*]|[0-9]{1,9}[.)])[ \t]+)*(?:#{1,6}[ \t]*)?$`)
	// blockStarter is text that would open a block at the start of a line.
	blockStarter = regexp.MustCompile(`^[ \t]*(?:[-+*][ \t]|#{1,6}(?:[ \t]|$)|>|=+[ \t\r]*$|` + "```" + `|~~~|[0-9]{1,9}[.)][ \t]|<[a-zA-Z!?/])`)
)

// removeNode takes a node out without creating Markdown from what surrounds it. A
// line left with only container markup goes whole. Where the joined text would
// form new syntax, such as `****`, a new link or a block marker, the node's plain
// text stays instead.
func removeNode(out *shaper, doc Document, i int, decide func(Occurrence) Rendition) {
	p := doc.parts[i]
	next := nextVisibleOffset(doc, i, decide)
	lineHead := lineStartIn(doc.Source, p.span.Start)
	rest := doc.Source[next:lineEnd(doc.Source, next)]
	if out.visible && isBlankLine(rest) && out.onlyTextOnLine() && emptiedLinePrefix.MatchString(doc.Source[lineHead:firstRemovedOnLine(doc, i, lineHead, decide)]) {
		out.retractLine()
		return
	}
	right := byte('\n')
	if next < doc.Body.End {
		right = doc.Source[next]
	}
	unsafe := false
	if out.visible {
		unsafe = !out.pendingSpace() && !safeJunction(out.lastByte(), right)
	} else {
		unsafe = blockStarter.MatchString(rest)
	}
	if unsafe && p.span.End-p.span.Start > 2 {
		out.text([]byte(doc.Source[p.span.Start+1 : p.span.End-1]))
		return
	}
	out.remove(closesSentence(right))
}

// nextVisibleOffset is the source offset after this node and any removed nodes
// directly following it.
func nextVisibleOffset(doc Document, i int, decide func(Occurrence) Rendition) int {
	end := doc.parts[i].span.End
	for j := i + 1; j < len(doc.parts); j++ {
		q := doc.parts[j]
		between := doc.Source[end:q.span.Start]
		if strings.TrimLeft(between, " \t") != "" || !partRemoved(doc, q, decide) {
			break
		}
		end = q.span.End
	}
	return end
}

// firstRemovedOnLine is where the run of removed nodes ending at part i starts.
func firstRemovedOnLine(doc Document, i, lineHead int, decide func(Occurrence) Rendition) int {
	start := doc.parts[i].span.Start
	for j := i - 1; j >= 0; j-- {
		q := doc.parts[j]
		if q.span.End < lineHead || strings.TrimLeft(doc.Source[q.span.End:start], " \t") != "" || !partRemoved(doc, q, decide) {
			break
		}
		start = q.span.Start
	}
	return start
}

func partRemoved(doc Document, p part, decide func(Occurrence) Rendition) bool {
	switch p.kind {
	case partDamaged:
		return true
	case partNode:
		for _, member := range doc.Occurrences[p.first : p.first+p.count] {
			if decide(member).Text != "" {
				return false
			}
		}
		return true
	case partLiteral, partLink:
	}
	return false
}

func lineStartIn(text string, at int) int {
	return strings.LastIndexByte(text[:at], '\n') + 1
}

// safeJunction reports whether text may close up around a removed node.
func safeJunction(left, right byte) bool {
	switch {
	case isSpaceOrNewline(right), closesSentence(right):
		return left != '<' && left != '['
	case left == '(' || left == '\'' || left == '"':
		return isWordByte(right)
	default:
		return false
	}
}

func closesSentence(b byte) bool {
	return isSpaceOrNewline(b) || strings.IndexByte(`.,;:!?)'"`, b) >= 0
}

func isSpaceOrNewline(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func isWordByte(b byte) bool {
	return b >= 0x80 || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

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
	`\`, `\\`, `[`, `\[`, `]`, `\]`, `*`, `\*`, `_`, `\_`, "`", "\\`", `<`, `\<`, `&`, `\&`, `~`, `\~`,
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

// PlainLabel is an unvalidated link's text with no link semantics, with any
// protocol node in it removed. A label that would autolink again is held in code.
func PlainLabel(link Link) string {
	label := strings.TrimSpace(Render(Parse(link.Label, Options{}), func(o Occurrence) Rendition {
		if o.Link != nil {
			return Rendition{Text: o.Link.Label}
		}
		return Rendition{}
	}))
	if label == "" {
		return ""
	}
	if strings.Contains(label, "://") || strings.HasPrefix(strings.ToLower(label), "www.") {
		return "`" + strings.Trim(label, "`") + "`"
	}
	return label
}
