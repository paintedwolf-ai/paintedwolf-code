package citationmd

import (
	"net/url"
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
// written, each protocol node as decide says, and damaged nodes never.
func Render(doc Document, decide func(Occurrence) Rendition) string {
	if doc.overBudget {
		return ""
	}
	var out shaper
	at := 0
	for _, p := range doc.parts {
		out.text([]byte(doc.Source[at:p.span.Start]))
		at = p.span.End
		switch p.kind {
		case partLiteral:
			out.literal([]byte(doc.Source[p.span.Start:p.span.End]))
		case partDamaged:
			out.remove()
		case partLink:
			renderLink(&out, doc, p, decide)
		case partNode:
			renderMembers(&out, doc.Occurrences[p.first:p.first+p.count], decide)
		}
	}
	out.text([]byte(doc.Source[at:]))
	out.close()
	return out.drain()
}

func renderLink(out *shaper, doc Document, p part, decide func(Occurrence) Rendition) {
	source := []byte(doc.Source[p.span.Start:p.span.End])
	if p.count == 0 {
		out.literal(source)
		return
	}
	switch rendition := decide(doc.Occurrences[p.first]); {
	case rendition.Keep:
		out.literal(source)
	case rendition.Text != "":
		out.text([]byte(rendition.Text))
	default:
		out.remove()
	}
}

// renderMembers joins a group's surviving members; a node with none is removed.
func renderMembers(out *shaper, members []Occurrence, decide func(Occurrence) Rendition) {
	var kept []string
	for _, member := range members {
		if text := decide(member).Text; text != "" {
			kept = append(kept, text)
		}
	}
	if len(kept) == 0 {
		out.remove()
		return
	}
	out.text([]byte(strings.Join(kept, ", ")))
}

// Project is the structural presentation that needs no evidence: explicit paths
// become file links, standard links stay, and the rest of the protocol is
// removed. The streaming projector converges on exactly this.
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

// PlainLabel is an unvalidated link's text with no link semantics. A label that
// would autolink again is held in code.
func PlainLabel(link Link) string {
	label := strings.TrimSpace(link.Label)
	if label == "" {
		return ""
	}
	if strings.Contains(label, "://") || strings.HasPrefix(strings.ToLower(label), "www.") {
		return "`" + strings.Trim(label, "`") + "`"
	}
	return label
}
