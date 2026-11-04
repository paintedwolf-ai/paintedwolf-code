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
	lineFragment = regexp.MustCompile(`^L?([0-9]+)(?:-L?([0-9]+))?$`)
)

func isWebDestination(dest string) bool { return webScheme.MatchString(dest) }

// Parse reads source into ordered occurrences, literal zones and explicit
// issues. Work is linear in source size except inside the structural parse, whose
// costly shapes are bounded. A source past those bounds keeps a structured issue
// and is scanned with coarse literal zones, so its display keeps the prose and
// fence-like regions and loses only protocol nodes.
func Parse(source string, opts Options) Document {
	doc := Document{Source: source, Body: Span{0, len(source)}}
	shape := structure{body: doc.Body}
	switch {
	case len(source) > opts.sourceBudget():
		doc.Issues = append(doc.Issues, Issue{Code: IssueSourceBudget, Token: boundedToken(source)})
		shape.zones = coarseLiteralZones(source)
	case structureExceeded(source):
		doc.Issues = append(doc.Issues, Issue{Code: IssueStructureBudget, Token: boundedToken(source)})
		shape.zones = coarseLiteralZones(source)
	default:
		parsed := documentStructure(source)
		if parsed.exhausted {
			doc.Issues = append(doc.Issues, Issue{Code: IssueStructureBudget, Token: boundedToken(source)})
			shape.zones = coarseLiteralZones(source)
		} else {
			shape = parsed
		}
	}
	doc.Body = shape.body
	builder := &documentBuilder{doc: &doc, budget: opts.occurrenceBudget(), opts: opts}
	machine := newNodeMachine(source, opts, builder)
	machine.skipTo(doc.Body.Start)
	scan := bodyScan{source: source, zones: shape.zones, links: shape.links, end: doc.Body.End, machine: machine, builder: builder}
	scan.run()
	for machine.finish() {
		scan.run()
	}
	builder.finishBudget()
	return doc
}

// bodyScan offers the document's non-literal text to the machine, passing literal
// zones and bare links.
type bodyScan struct {
	source  string
	zones   []Zone
	links   map[int]*Link
	next    int
	end     int
	runEnd  int
	machine *nodeMachine
	builder *documentBuilder
}

func (s *bodyScan) run() {
	m := s.machine
	for m.pos < s.end {
		at := m.pos
		for s.next > 0 && s.zones[s.next-1].Span.End > at {
			s.next--
		}
		for s.next < len(s.zones) && s.zones[s.next].Span.End <= at {
			s.next++
		}
		if m.idle() && s.next < len(s.zones) && s.zones[s.next].Span.Start <= at {
			s.enterZone(at)
			continue
		}
		if m.idle() && !m.escaped && s.enterBareLink(at) {
			continue
		}
		m.feed(s.source[at])
	}
}

// urlTailEnd finds where a run of URL bytes ends, measuring each run only once.
func (s *bodyScan) urlTailEnd(at int) int {
	if at < s.runEnd {
		return s.runEnd
	}
	end := at
	for end < s.end && !isURLStop(s.source[end]) {
		end++
	}
	s.runEnd = end
	return end
}

func isURLStop(b byte) bool {
	return b == '<' || b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

func (s *bodyScan) enterZone(at int) {
	zone := s.zones[s.next]
	link := s.links[zone.Span.Start]
	// A node that ran into this zone took its start; the rest stays literal.
	if zone.Span.Start < at {
		zone.Span.Start, link = at, nil
	}
	s.builder.addZone(zone, link)
	s.machine.skipTo(zone.Span.End)
}

// enterBareLink takes a bare URL as an autolink zone. In a prose bracket such as
// `[https://x]` the closing bracket is the bracket's, not the URL's.
func (s *bodyScan) enterBareLink(at int) bool {
	url := bareLinkAt(s.source[at:s.end], func() int { return s.urlTailEnd(at) - at })
	if url == "" {
		return false
	}
	if strings.HasSuffix(url, "]") && at > 0 && s.source[at-1] == '[' {
		url = url[:len(url)-1]
	}
	span := Span{at, at + len(url)}
	var link *Link
	if dest := bareLinkDestination(url); isWebDestination(dest) {
		link = &Link{Form: LinkAutolink, Label: url, LabelSpan: span, Destination: dest}
	}
	s.builder.addZone(Zone{Kind: ZoneAutolink, Span: span}, link)
	s.machine.skipTo(span.End)
	return true
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
	b.addIssue(issue)
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
	for _, defect := range node.defects {
		member := Span{start + defect.Start, start + defect.End}
		b.addIssue(Issue{Code: IssueMalformedCitation, Kind: KindCitation, Span: member, Token: boundedToken(b.doc.Source[member.Start:member.End])})
	}
	b.doc.parts = append(b.doc.parts, part{kind: partNode, span: span, first: first, count: len(node.members)})
}

// addIssue records a defect up to the budget; past it, the count is what reports.
func (b *documentBuilder) addIssue(issue Issue) {
	if len(b.doc.Issues) >= b.budget {
		b.dropped++
		return
	}
	b.doc.Issues = append(b.doc.Issues, issue)
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
		b.addIssue(Issue{Code: IssueConflictingVerification, Kind: KindVerify, Span: o.Span, Token: boundedToken(o.Token)})
	}
}

func (b *documentBuilder) addZone(zone Zone, link *Link) {
	b.doc.Zones = append(b.doc.Zones, zone)
	if zone.Kind == ZoneRawHTML || zone.Kind == ZoneHTMLBlock {
		b.addRawZone(zone)
		return
	}
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

// addRawZone splits raw HTML around anchors that render live links, so that each
// is validated like any other link and the rest stays literal.
func (b *documentBuilder) addRawZone(zone Zone) {
	at := zone.Span.Start
	for _, tag := range htmlAnchors(b.doc.Source, zone.Span) {
		if tag.Start > at {
			b.doc.parts = append(b.doc.parts, part{kind: partLiteral, span: Span{at, tag.Start}})
		}
		at = tag.End
		link := &Link{Form: LinkRawHTML, LabelSpan: Span{tag.End, tag.End}, Destination: htmlAnchorHref(b.doc.Source[tag.Start:tag.End])}
		first, count := 0, 0
		if occurrence, ok := b.linkOccurrence(Zone{Kind: zone.Kind, Span: tag}, link); ok {
			first, count = len(b.doc.Occurrences), 1
			occurrence.Index = first
			b.doc.Occurrences = append(b.doc.Occurrences, occurrence)
		}
		b.doc.parts = append(b.doc.parts, part{kind: partLink, span: tag, first: first, count: count})
	}
	if at < zone.Span.End {
		b.doc.parts = append(b.doc.parts, part{kind: partLiteral, span: Span{at, zone.Span.End}})
	}
}

// linkOccurrence interprets a link's destination once. Its label never yields
// a citation. A workspace link with a coordinate is an attempt; without one it is
// a citation only on an observed path. Other schemes and same-document anchors
// are not citations.
func (b *documentBuilder) linkOccurrence(zone Zone, link *Link) (Occurrence, bool) {
	occurrence := Occurrence{Span: zone.Span, Node: zone.Span, Token: b.doc.Source[zone.Span.Start:zone.Span.End], Link: link}
	dest := link.Destination
	switch {
	case isWebDestination(dest):
		occurrence.Kind = KindWebLink
	case isSourceDestination(dest):
		citation, code := sourceCitation(dest, b.opts)
		if code != "" {
			b.linkIssue(code, zone, occurrence.Token)
			return Occurrence{}, false
		}
		occurrence.Kind, occurrence.Citation = KindWorkspaceLink, citation
	case dest == "", strings.HasPrefix(dest, "#"), anyScheme.MatchString(dest):
		return Occurrence{}, false
	default:
		citation, code := workspaceCitation(dest)
		if code != "" {
			b.linkIssue(code, zone, occurrence.Token)
			return Occurrence{}, false
		}
		occurrence.Kind, occurrence.Citation = KindWorkspaceLink, citation
	}
	if occurrence.Citation != nil && occurrence.Citation.Line == 0 && !b.opts.known(occurrence.Citation.Path) {
		return Occurrence{}, false
	}
	if len(b.doc.Occurrences) >= b.budget {
		b.dropped++
		return Occurrence{}, false
	}
	return occurrence, true
}

func (b *documentBuilder) linkIssue(code IssueCode, zone Zone, token string) {
	b.addIssue(Issue{Code: code, Kind: KindWorkspaceLink, Span: zone.Span, Token: boundedToken(token)})
}

// workspaceCitation reads a destination's path and coordinates. An unencoded `#`
// or `?` ends the path, so a heading anchor binds the file; only a line fragment
// is a coordinate, and the label never is.
func workspaceCitation(dest string) (*Citation, IssueCode) {
	c := &Citation{}
	path, fragment, _ := strings.Cut(dest, "#")
	path, _, _ = strings.Cut(path, "?")
	if match := lineFragment.FindStringSubmatch(fragment); match != nil {
		if code := setLines(c, match[1], match[2]); code != "" {
			return nil, code
		}
	}
	c.Path = unescapePath(path)
	return c, ""
}

func isSourceDestination(dest string) bool {
	return len(dest) >= len("source:") && strings.EqualFold(dest[:len("source:")], "source:")
}

// sourceCitation reads `source://root-id/path#Lstart-Lend` into workspace form. A
// query other than a job id is a malformed link, as in host navigation.
func sourceCitation(dest string, opts Options) (*Citation, IssueCode) {
	parsed, err := url.Parse(dest)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, IssueMalformedCitation
	}
	rel := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if rel == "" {
		return nil, IssueMalformedCitation
	}
	query := parsed.Query()
	if len(query) > 1 || (len(query) == 1 && query.Get("job_id") == "") {
		return nil, IssueMalformedCitation
	}
	if parsed.Fragment != "" {
		rel += "#" + parsed.Fragment
	}
	citation, code := workspaceCitation(rel)
	if code != "" {
		return nil, code
	}
	if opts.SourceRoot != nil {
		prefix, ok := opts.SourceRoot(parsed.Host)
		if !ok {
			citation.Path = "source://" + parsed.Host + "/" + citation.Path
			return citation, ""
		}
		citation.Path = prefix + citation.Path
	}
	return citation, ""
}

func (b *documentBuilder) finishBudget() {
	if b.dropped > 0 {
		b.doc.Issues = append(b.doc.Issues, Issue{Code: IssueOccurrenceBudget, Token: strconv.Itoa(b.dropped)})
	}
}
