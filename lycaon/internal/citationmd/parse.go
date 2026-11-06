package citationmd

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	webScheme    = regexp.MustCompile(`(?i)^[ \t]*https?:`)
	anyScheme    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	lineFragment = regexp.MustCompile(`^L([0-9]+)(?:-L?([0-9]+))?$`)
)

// isWebDestination takes any spelling a browser opens as HTTP(S), including one
// with no slashes.
func isWebDestination(dest string) bool { return webScheme.MatchString(dest) }

// Parse reads source into ordered occurrences, literal zones and explicit issues.
// Work is linear in source size except inside the structural parse, whose costly
// shapes are refused first. A refused or over-budget source keeps a structured issue
// and displays as one literal block; nothing is taken from it as a citation.
func Parse(source string, opts Options) Document {
	doc := Document{Source: source, Body: Span{0, len(source)}}
	var code IssueCode
	var shape structure
	switch {
	case len(source) > opts.sourceBudget():
		code = IssueSourceBudget
	case structureExceeded(source):
		code = IssueStructureBudget
	default:
		if shape = documentStructure(source); shape.exhausted {
			code = IssueStructureBudget
		}
	}
	if code != "" {
		doc.Issues = append(doc.Issues, Issue{Code: code, Token: boundedToken(source)})
		doc.overBudget = true
		doc.Zones = []Zone{{Kind: ZoneEscapedDocument, Span: doc.Body}}
		return doc
	}
	doc.Body = shape.body
	doc.Images = shape.images
	builder := &documentBuilder{doc: &doc, budget: opts.occurrenceBudget(), opts: opts}
	machine := newNodeMachine(source, opts, builder)
	machine.skipTo(doc.Body.Start)
	builder.definitions = shape.definitions
	scan := bodyScan{source: source, zones: shape.zones, links: shape.links, end: doc.Body.End, machine: machine, builder: builder, lastOpen: -1}
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
	source string
	zones  []Zone
	links  map[int]*Link
	next   int
	end    int
	// runStart and runEnd bound the last measured run of URL bytes. A released
	// bracket can move the scan back before it.
	runStart int
	runEnd   int
	// lastOpen is the latest `[` still open on its line, or -1.
	lastOpen int
	machine  *nodeMachine
	builder  *documentBuilder
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
		if m.idle() && s.enterBareLink(at) {
			continue
		}
		// Only idle text counts: a node attempt that is released is scanned again.
		if m.idle() {
			switch s.source[at] {
			case '[':
				s.lastOpen = at
			case ']':
				s.lastOpen = -1
			}
		}
		m.feed(s.source[at])
	}
}

// urlTailEnd finds where a run of URL bytes ends, measuring each run only once.
func (s *bodyScan) urlTailEnd(at int) int {
	if at < s.runStart || at >= s.runEnd {
		s.runStart = at
		s.runEnd = at + urlRunEnd(s.source[at:s.end])
	}
	return s.runEnd
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

// enterBareLink takes a bare URL as an autolink zone. A `]` ending it closes a
// surrounding bracket on the line rather than belonging to the URL. A URL past the
// node cap is one rejected unit: validating a prefix would vouch for the whole.
func (s *bodyScan) enterBareLink(at int) bool {
	run := bareLinkRun(s.source[at:s.end], func() int { return s.urlTailEnd(at) - at })
	if run == 0 {
		return false
	}
	if run > MaxInlineProtocolNodeBytes {
		span := Span{at, at + run}
		s.builder.damaged(Issue{Code: IssueNodeTooLong, Kind: KindWebLink, Span: span, Token: boundedToken(s.source[span.Start:span.End])})
		s.machine.skipTo(span.End)
		return true
	}
	url := backpedalURL(s.source[at : at+run])
	if url == "" {
		return false
	}
	if strings.HasSuffix(url, "]") && s.lastOpen >= 0 && s.lastOpen < at && strings.IndexByte(s.source[s.lastOpen:at], '\n') < 0 {
		url = url[:len(url)-1]
		s.lastOpen = -1
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
	// brokenLink marks the link in hand as one that raised an issue.
	brokenLink  bool
	definitions map[int]string
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
		b.canonicalize(member.citation)
		if member.repair != nil {
			b.canonicalize(member.repair.Citation)
		}
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

func (b *documentBuilder) canonicalize(c *Citation) {
	if c != nil && c.Path != "" && b.opts.CanonicalPath != nil {
		c.Path = b.opts.CanonicalPath(c.Path)
	}
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
	if zone.Kind == ZoneCodeSpan && b.addCodeCitation(zone) {
		return
	}
	entry := part{kind: literalPartKind(zone.Kind), span: zone.Span, definition: b.definitions[zone.Span.Start]}
	if link != nil {
		entry.kind = partLink
		entry.link = link
		b.brokenLink = false
		if occurrence, ok := b.linkOccurrence(zone, link); ok {
			entry.first, entry.count = len(b.doc.Occurrences), 1
			occurrence.Index = entry.first
			b.doc.Occurrences = append(b.doc.Occurrences, occurrence)
		}
		entry.broken = b.brokenLink
	}
	b.doc.parts = append(b.doc.parts, entry)
}

// literalPartKind separates code, which stays byte for byte, from raw HTML, which
// the display still has to neutralize.
func literalPartKind(kind ZoneKind) partKind {
	switch kind {
	case ZoneRawHTML:
		return partRawHTML
	case ZoneHTMLBlock:
		return partHTMLBlock
	default:
		return partLiteral
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
	b.brokenLink = true
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
		b.doc.overBudget = true
	}
}
