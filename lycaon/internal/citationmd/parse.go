package citationmd

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	webScheme    = regexp.MustCompile(`(?i)^https?://`)
	anyScheme    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	lineFragment = regexp.MustCompile(`#L?([0-9]+)(?:-L?([0-9]+))?$`)
)

func isWebDestination(dest string) bool { return webScheme.MatchString(dest) }

// Parse reads source into ordered occurrences, literal zones and explicit
// issues. Work is linear in source size except inside the structural parse, whose
// costly shapes are bounded. A source past those bounds keeps a structured issue
// and is scanned with no literal zones, so its display keeps the prose and loses
// only protocol nodes.
func Parse(source string, opts Options) Document {
	doc := Document{Source: source, Body: Span{0, len(source)}}
	shape := structure{body: doc.Body}
	switch {
	case len(source) > opts.sourceBudget():
		doc.Issues = append(doc.Issues, Issue{Code: IssueSourceBudget, Token: boundedToken(source)})
	case containerDepthExceeded(source):
		doc.Issues = append(doc.Issues, Issue{Code: IssueStructureBudget, Token: boundedToken(source)})
	default:
		parsed := documentStructure(source)
		if parsed.exhausted {
			doc.Issues = append(doc.Issues, Issue{Code: IssueStructureBudget, Token: boundedToken(source)})
		} else {
			shape = parsed
		}
	}
	doc.Body = shape.body
	builder := &documentBuilder{doc: &doc, budget: opts.occurrenceBudget(), opts: opts}
	machine := newNodeMachine(opts, builder)
	machine.skipTo(doc.Body.Start)
	zones := shape.zones
	for at := doc.Body.Start; at < doc.Body.End; {
		for len(zones) > 0 && zones[0].Span.End <= at {
			zones = zones[1:]
		}
		if len(zones) > 0 && machine.idle() && zones[0].Span.Start <= at {
			zone := zones[0]
			link := shape.links[zone.Span.Start]
			// A node that ran into this zone took its start; the rest stays literal.
			if zone.Span.Start < at {
				zone.Span.Start, link = at, nil
			}
			builder.addZone(zone, link)
			at = zone.Span.End
			machine.skipTo(at)
			continue
		}
		if machine.feed(source[at]) {
			at++
		}
	}
	machine.finish()
	builder.finishBudget()
	return doc
}

// documentBuilder assembles a Document from zones and machine events.
type documentBuilder struct {
	doc     *Document
	opts    Options
	budget  int
	dropped int
	verify  *Occurrence
}

func (b *documentBuilder) damaged(issue Issue) {
	b.doc.Issues = append(b.doc.Issues, issue)
	b.doc.parts = append(b.doc.parts, part{kind: partDamaged, span: issue.Span})
}

func (b *documentBuilder) closed(token []byte, node parsedNode, start int) {
	span := Span{start, start + len(token)}
	if len(b.doc.Occurrences)+len(node.members) > b.budget {
		b.dropped += len(node.members)
		b.doc.parts = append(b.doc.parts, part{kind: partNode, span: span})
		return
	}
	first := len(b.doc.Occurrences)
	for _, member := range node.members {
		occurrence := member.occurrence()
		occurrence.Index, occurrence.Span, occurrence.Node = len(b.doc.Occurrences), span, span
		if member.end > member.start {
			occurrence.Span = Span{start + member.start, start + member.end}
		}
		occurrence.Token = b.doc.Source[occurrence.Span.Start:occurrence.Span.End]
		b.checkVerification(occurrence)
		b.doc.Occurrences = append(b.doc.Occurrences, occurrence)
	}
	b.doc.parts = append(b.doc.parts, part{kind: partNode, span: span, first: first, count: len(node.members)})
}

// checkVerification rejects a second assessment that differs from the first.
func (b *documentBuilder) checkVerification(o Occurrence) {
	if o.Verify == nil {
		return
	}
	if b.verify == nil {
		copied := o
		b.verify = &copied
		return
	}
	if *b.verify.Verify != *o.Verify {
		b.doc.Issues = append(b.doc.Issues, Issue{Code: IssueConflictingVerification, Kind: KindVerify, Span: o.Span, Token: boundedToken(o.Token)})
	}
}

func (b *documentBuilder) addZone(zone Zone, link *Link) {
	b.doc.Zones = append(b.doc.Zones, zone)
	kind := partLiteral
	first, count := 0, 0
	if link != nil {
		kind = partLink
		if occurrence, ok := b.linkOccurrence(zone, link); ok {
			first, count = len(b.doc.Occurrences), 1
			occurrence.Index = first
			b.doc.Occurrences = append(b.doc.Occurrences, occurrence)
		}
	}
	b.doc.parts = append(b.doc.parts, part{kind: kind, span: zone.Span, first: first, count: count})
}

// linkOccurrence interprets a link's destination once. Its label never yields
// a citation. Other schemes and same-document anchors are not citations.
func (b *documentBuilder) linkOccurrence(zone Zone, link *Link) (Occurrence, bool) {
	occurrence := Occurrence{Span: zone.Span, Node: zone.Span, Token: b.doc.Source[zone.Span.Start:zone.Span.End], Link: link}
	dest := link.Destination
	switch {
	case isWebDestination(dest):
		occurrence.Kind = KindWebLink
	case isSourceDestination(dest):
		citation, ok := sourceCitation(dest, b.opts)
		if !ok {
			return Occurrence{}, false
		}
		occurrence.Kind = KindWorkspaceLink
		occurrence.Citation = citation
	case dest == "", strings.HasPrefix(dest, "#"), anyScheme.MatchString(dest):
		return Occurrence{}, false
	default:
		occurrence.Kind = KindWorkspaceLink
		occurrence.Citation = workspaceCitation(dest)
	}
	if len(b.doc.Occurrences) >= b.budget {
		b.dropped++
		return Occurrence{}, false
	}
	return occurrence, true
}

// workspaceCitation reads coordinates from the destination, never the label.
func workspaceCitation(dest string) *Citation {
	c := &Citation{}
	if match := lineFragment.FindStringSubmatchIndex(dest); match != nil {
		start := dest[match[2]:match[3]]
		end := ""
		if match[4] >= 0 {
			end = dest[match[4]:match[5]]
		}
		// A malformed bound leaves no coordinate for the resolver to trust.
		if setLines(c, start, end) == "" {
			dest = dest[:match[0]]
		} else {
			*c = Citation{}
		}
	}
	c.Path = unescapePath(dest)
	return c
}

func isSourceDestination(dest string) bool {
	return len(dest) >= len("source:") && strings.EqualFold(dest[:len("source:")], "source:")
}

// sourceCitation reads `source://root-id/path#Lstart-Lend` into workspace form.
func sourceCitation(dest string, opts Options) (*Citation, bool) {
	parsed, err := url.Parse(dest)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, false
	}
	rel := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if rel == "" {
		return nil, false
	}
	if parsed.Fragment != "" {
		rel += "#" + parsed.Fragment
	}
	citation := workspaceCitation(rel)
	prefix := "@" + parsed.Host + "/"
	if opts.SourceRoot == nil {
		prefix = ""
	} else if mapped, ok := opts.SourceRoot(parsed.Host); ok {
		prefix = mapped
	}
	citation.Path = prefix + citation.Path
	return citation, true
}

func (b *documentBuilder) finishBudget() {
	if b.dropped > 0 {
		b.doc.Issues = append(b.doc.Issues, Issue{Code: IssueOccurrenceBudget, Token: strconv.Itoa(b.dropped)})
	}
}
