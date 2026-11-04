package citationmd

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/verification"
)

var (
	handleWithCoordinate = regexp.MustCompile(`^(` + hostmarker.EvidenceHandlePattern + `)(?::([0-9]+)(?:-([0-9]+))?)?$`)
	coordinateTail       = regexp.MustCompile(`:([0-9]+)(?:-([0-9]+))?$`)
	artifactID           = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	repairTarget         = regexp.MustCompile(`^c[1-9][0-9]*$`)
)

// parsedMember is one typed member of a node, with its span inside the node.
type parsedMember struct {
	kind     NodeKind
	start    int
	end      int
	citation *Citation
	artifact string
	verify   *verification.Assessment
	repair   *Repair
}

func (m parsedMember) occurrence() Occurrence {
	return Occurrence{Kind: m.kind, Citation: m.citation, Artifact: m.artifact, Verify: m.verify, Repair: m.repair}
}

// parsedNode is a closed node's meaning. prose means the bracket turned out to
// be ordinary Markdown; issue means it is a damaged node. defects are spans of
// members that are no citation inside a group that is one.
type parsedNode struct {
	members []parsedMember
	defects []Span
	issue   IssueCode
	kind    NodeKind
	prose   bool
}

// parseNode interprets a complete bracketed node under the scanner's mode.
func parseNode(token string, mode bodyMode, attempt, indexed bool, opts Options) parsedNode {
	body := token[1 : len(token)-1]
	switch mode {
	case modeVerify:
		return parseVerify(body)
	case modeArtifact:
		return parseArtifact(body)
	case modeRepair:
		return parseRepair(body)
	case modeLexeme, modeCitation:
	}
	return parseCitationGroup(body, attempt, indexed, opts)
}

// parseCitationGroup decides a group as a group. With any attempt among its
// members, the others are defects; with none, the bracket is prose.
func parseCitationGroup(body string, attempt, indexed bool, opts Options) parsedNode {
	node := parsedNode{kind: KindCitation}
	r := memberReader{body: body, gate: &memberGate{opts: opts, indexed: indexed}}
	for {
		start := r.skipSpace()
		citation, code := r.member()
		end := start + len(strings.TrimRight(body[start:r.at], " \t\r"))
		switch {
		case code != "" && (attempt || code == IssueInvalidRange):
			return parsedNode{issue: code, kind: KindCitation}
		case code != "":
			return parsedNode{prose: true, kind: KindCitation}
		case citation != nil:
			node.members = append(node.members, parsedMember{kind: KindCitation, start: start + 1, end: end + 1, citation: citation})
		default:
			node.defects = append(node.defects, Span{start + 1, end + 1})
		}
		r.skipSpace()
		if r.done() {
			break
		}
		if body[r.at] != ',' {
			if attempt {
				return parsedNode{issue: IssueMalformedCitation, kind: KindCitation}
			}
			return parsedNode{prose: true, kind: KindCitation}
		}
		r.at++
	}
	if len(node.members) == 0 {
		return parsedNode{prose: true, kind: KindCitation}
	}
	return node
}

// memberGate decides whether an ordinary path member is a citation attempt. A nil
// gate means the context already makes every path explicit.
type memberGate struct {
	opts    Options
	indexed bool
}

// memberReader walks a citation body outside and across quoted strings.
type memberReader struct {
	body string
	at   int
	gate *memberGate
}

func (r *memberReader) done() bool { return r.at >= len(r.body) }

func (r *memberReader) skipSpace() int {
	for !r.done() && (r.body[r.at] == ' ' || r.body[r.at] == '\t') {
		r.at++
	}
	return r.at
}

// lexeme reads an unquoted run up to whitespace or a separator.
func (r *memberReader) lexeme() string {
	start := r.at
	for !r.done() && !strings.ContainsRune(" \t,", rune(r.body[r.at])) {
		r.at++
	}
	return r.body[start:r.at]
}

// quoted reads a JSON string at the cursor. It also takes the Markdown escapes
// `\\[` and `\\]`, which are not JSON escapes.
func (r *memberReader) quoted() (string, bool) {
	raw := []byte{'"'}
	r.at++
	for ; !r.done(); r.at++ {
		c := r.body[r.at]
		switch {
		case c == '"':
			r.at++
			var out string
			if err := json.Unmarshal(append(raw, c), &out); err != nil {
				return "", false
			}
			return out, true
		case c == '\\' && r.at+1 < len(r.body):
			r.at++
			if next := r.body[r.at]; next == '[' || next == ']' {
				raw = append(raw, next)
			} else {
				raw = append(raw, c, next)
			}
		default:
			raw = append(raw, c)
		}
	}
	return "", false
}

func (r *memberReader) atQuote() bool { return !r.done() && r.body[r.at] == '"' }

// member parses `handle[:lines] [path[:lines]] ["excerpt"]` or
// `path[:lines] ["excerpt"]`. A well-formed member that is no attempt yields nil
// with no code.
func (r *memberReader) member() (*Citation, IssueCode) {
	c := &Citation{}
	if !r.atQuote() {
		saved := r.at
		lexeme := r.lexeme()
		if match := handleWithCoordinate.FindStringSubmatch(lexeme); match != nil {
			if r.gate != nil && !r.gate.opts.knownKind(terminalKind(match[1])) {
				return nil, ""
			}
			c.Handle = match[1]
			if code := setLines(c, match[2], match[3]); code != "" {
				return nil, code
			}
			return r.afterHandle(c)
		}
		if strings.Contains(lexeme, "#") {
			if r.gate != nil && !malformedCoordinate.MatchString(lexeme) && !r.gate.opts.knownKind(terminalKind(lexeme[:strings.IndexByte(lexeme, '#')])) {
				return nil, ""
			}
			return nil, IssueMalformedCitation
		}
		r.at = saved
	}
	attempt, code := r.pathRef(c, r.gate)
	if code != "" {
		return nil, code
	}
	citation, code := r.excerpt(c)
	if !attempt {
		return nil, code
	}
	return citation, code
}

func terminalKind(handle string) string {
	kind, _, _ := strings.Cut(handle, "#")
	return kind[strings.LastIndexByte(kind, ':')+1:]
}

func (r *memberReader) afterHandle(c *Citation) (*Citation, IssueCode) {
	if r.skipSpace(); r.done() || r.body[r.at] == ',' {
		return c, ""
	}
	if r.atQuote() {
		saved := r.at
		text, ok := r.quoted()
		if !ok {
			return nil, IssueMalformedCitation
		}
		if r.done() || r.body[r.at] != ':' {
			c.Excerpt = text
			return c, ""
		}
		r.at = saved
	}
	if _, code := r.pathRef(c, nil); code != "" {
		return nil, code
	}
	return r.excerpt(c)
}

// pathRef reads a quoted or bare path with an optional coordinate and reports
// whether it is a citation attempt. A quoted path needs a coordinate or a ledger
// match, like any other; strings in prose are common.
func (r *memberReader) pathRef(c *Citation, gate *memberGate) (attempt bool, code IssueCode) {
	var path, start, end string
	if r.atQuote() {
		text, ok := r.quoted()
		if !ok || text == "" {
			return false, IssueMalformedCitation
		}
		path = text
		if !r.done() && r.body[r.at] == ':' {
			tail := r.lexeme()
			match := coordinateTail.FindStringSubmatch(tail)
			if len(match) == 0 || match[0] != tail {
				return false, IssueMalformedCitation
			}
			start, end = match[1], match[2]
		}
		attempt = gate == nil || (!gate.indexed && (start != "" || gate.opts.known(path)))
	} else {
		lexeme := r.lexeme()
		var ok bool
		path, start, end, ok = splitCoordinate(lexeme)
		if !ok || !validBarePath(path) {
			return false, IssueMalformedCitation
		}
		attempt = gate == nil || isCitationAttempt(path, start, gate.indexed, gate.opts)
	}
	if !attempt {
		return false, ""
	}
	c.Path = path
	if start != "" && c.Line != 0 {
		return false, IssueMalformedCitation
	}
	return true, setLines(c, start, end)
}

func (r *memberReader) excerpt(c *Citation) (*Citation, IssueCode) {
	if r.skipSpace(); !r.atQuote() {
		return c, ""
	}
	text, ok := r.quoted()
	if !ok {
		return nil, IssueMalformedCitation
	}
	c.Excerpt = text
	return c, ""
}

// setLines applies a coordinate; bounds must be positive and ordered.
func setLines(c *Citation, start, end string) IssueCode {
	if start == "" {
		return ""
	}
	line, err := strconv.Atoi(start)
	if err != nil || line <= 0 || line > MaxLineNumber {
		return IssueInvalidRange
	}
	c.Line = line
	if end == "" {
		return ""
	}
	last, err := strconv.Atoi(end)
	if err != nil || last < line || last > MaxLineNumber {
		return IssueInvalidRange
	}
	if last > line {
		c.EndLine = last
	}
	return ""
}

// splitCoordinate separates a trailing `:line[-line]`. A colon elsewhere means
// the lexeme is not a bare path.
func splitCoordinate(lexeme string) (path, start, end string, ok bool) {
	path = lexeme
	if match := coordinateTail.FindStringSubmatchIndex(lexeme); match != nil {
		path = lexeme[:match[0]]
		start = lexeme[match[2]:match[3]]
		if match[4] >= 0 {
			end = lexeme[match[4]:match[5]]
		}
	}
	return path, start, end, path != "" && !strings.Contains(path, ":")
}

// validBarePath checks the workspace-path grammar by rune. It carries no
// extension or vocabulary rule.
func validBarePath(path string) bool {
	for _, r := range path {
		switch {
		case r == unicode.ReplacementChar:
			return false
		case unicode.IsLetter(r), unicode.IsMark(r), unicode.IsDigit(r):
		case r < 0x80 && strings.ContainsRune("-_.@+~%=/", r):
		default:
			return false
		}
	}
	return path != ""
}

// isStructuralBracket reports Markdown's own bracket uses: task boxes, numeric
// indices and footnote markers. The scanner rejects `[ ` and `[^` by their first
// byte, so only these bodies arrive complete.
func isStructuralBracket(body string) bool {
	return body == "x" || body == "X" || isAllDigits(body)
}

func isAllDigits(text string) bool {
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return text != ""
}
