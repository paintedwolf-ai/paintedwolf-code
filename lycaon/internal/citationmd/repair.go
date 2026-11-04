package citationmd

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Token is the citation's canonical body, without brackets. A path with no
// coordinate is quoted, because an unquoted one is a citation only when observed.
func (c Citation) Token() string {
	var b strings.Builder
	b.WriteString(c.Handle)
	if c.Path != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if validBarePath(c.Path) && c.Line > 0 {
			b.WriteString(c.Path)
		} else {
			b.WriteString(quoted(c.Path))
		}
	}
	if c.Line > 0 {
		b.WriteString(":" + strconv.Itoa(c.Line))
		if c.EndLine > c.Line {
			b.WriteString("-" + strconv.Itoa(c.EndLine))
		}
	}
	if c.Excerpt != "" {
		b.WriteString(" " + quoted(c.Excerpt))
	}
	return b.String()
}

func quoted(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// Narrative is the document's text with every protocol node removed and each
// labelled link reduced to its label, whitespace collapsed. A repair must leave
// it unchanged; empty means the document states nothing.
func Narrative(doc Document) string {
	rendered := Render(doc, func(o Occurrence) Rendition {
		if o.Link != nil && o.Link.Form != LinkAutolink {
			return Rendition{Text: PlainLabel(*o.Link)}
		}
		return Rendition{}
	})
	return strings.Join(strings.Fields(rendered), " ")
}

// RetargetLink is a link occurrence's source with a new destination and its label
// kept.
func RetargetLink(doc Document, o Occurrence, destination string) string {
	if o.Link == nil || o.Link.Form == LinkAutolink {
		return "<" + destination + ">"
	}
	span := o.Link.LabelSpan
	if strings.ContainsAny(destination, " ()\t") {
		destination = "<" + destination + ">"
	}
	return "[" + doc.Source[span.Start:span.End] + "](" + destination + ")"
}

// RetargetWorkspaceLink is a workspace link's source pointing at a new path and
// coordinates, its label kept.
func RetargetWorkspaceLink(doc Document, o Occurrence, c Citation) string {
	fragment := ""
	if c.Line > 0 {
		fragment = "#L" + strconv.Itoa(c.Line)
		if c.EndLine > c.Line {
			fragment += "-L" + strconv.Itoa(c.EndLine)
		}
	}
	return RetargetLink(doc, o, fileDestination(c.Path, fragment))
}

// LabelWithCitation is a workspace link's label as text, followed by a citation: a
// handle names no destination for the link to keep.
func LabelWithCitation(doc Document, o Occurrence, c Citation) string {
	span := o.Link.LabelSpan
	return doc.Source[span.Start:span.End] + " [" + c.Token() + "]"
}

// ZoneText is a zone's source text.
func (d Document) ZoneText(z Zone) string { return d.Source[z.Span.Start:z.Span.End] }
