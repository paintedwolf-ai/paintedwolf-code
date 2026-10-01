package evidence

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	summarizeKindDirectoryMap    = "directory_map"
	summarizeKindDirectoryRollup = "directory_rollup"
)

type summarizeWireRoot struct {
	Task    string                `json:"task"`
	Pack    summarizePackWire     `json:"pack"`
	Anchors []summarizeWireAnchor `json:"anchors"`
}

type summarizePackWire struct {
	Identity  []summarizePackIdentityWire `json:"identity"`
	Skeleton  []summarizePackSymbolWire   `json:"skeleton"`
	Substance []summarizePackWindowWire   `json:"substance"`
	CallSites []summarizePackCallSiteWire `json:"call_sites"`
}

type summarizePackIdentityWire struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type summarizePackSymbolWire struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

type summarizePackWindowWire struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Symbol    string `json:"symbol"`
	Body      string `json:"body"`
}

type summarizePackCallSiteWire struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

// populateSummarizePackRecord indexes summarize pack material on the parent tool
// record so pack-only responses (empty anchors[]) still admit citable paths and a
// task line for parent-handle citations.
func populateSummarizePackRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	root, ok := parseSummarizeWire(content)
	if !ok {
		return
	}
	task := strings.TrimSpace(root.Task)
	if task != "" {
		rec.Body = append(rec.Body, task)
	}
	for _, id := range root.Pack.Identity {
		path := strings.TrimSpace(id.Path)
		if path == "" {
			continue
		}
		if id.Kind == summarizeKindDirectoryMap || id.Kind == summarizeKindDirectoryRollup {
			rec.Survey = true
		}
		addRecordPath(projectDir, rec, path, FidelityStructured)
	}
	for _, cs := range root.Pack.CallSites {
		indexSummarizeSpan(projectDir, rec, cs.Path, cs.Line, cs.Excerpt)
	}
	for _, w := range root.Pack.Substance {
		path := strings.TrimSpace(w.Path)
		if path == "" || w.StartLine <= 0 {
			continue
		}
		end := w.EndLine
		if end < w.StartLine {
			end = w.StartLine
		}
		appendLineRange(rec, w.StartLine, end)
		addRecordPath(projectDir, rec, path, FidelityStructured)
		for _, line := range substanceBodyLines(w.Body) {
			rel, ok := NormalizeCitationPath(projectDir, path)
			if !ok || line.line <= 0 {
				continue
			}
			rec.addGrepLine(rel, line.line, line.content)
		}
	}
	for _, s := range root.Pack.Skeleton {
		if !summarizeSkeletonCitable(s) {
			continue
		}
		excerpt := summarizeSkeletonExcerpt(s, root.Pack.Substance)
		indexSummarizeSpan(projectDir, rec, s.Path, s.Line, excerpt)
	}
}

// SummarizePackHighlightRecords builds child highlight records from pack material
// when anchors[] is empty — parity with anchor minting at evidence commit.
func SummarizePackHighlightRecords(projectDir, content string) []Record {
	root, ok := parseSummarizeWire(content)
	if !ok {
		return nil
	}
	binding := ActiveBinding()
	kind := binding.ToolKind("summarize")
	shape := binding.ShapeForToolKind("summarize", kind)
	var out []Record
	appendRec := func(path string, line int, excerpt string) {
		path = strings.TrimSpace(path)
		excerpt = strings.TrimSpace(excerpt)
		if path == "" || line <= 0 || excerpt == "" {
			return
		}
		rec := Record{
			Kind:       kind,
			Shape:      shape,
			SourceTool: "summarize",
			Survey:     false,
			Path:       path,
			LineRanges: []LineRange{{Start: line, End: line}},
			Body:       []string{excerpt},
			Fidelity:   FidelityStructured,
		}
		rel := path
		if norm, ok := NormalizeCitationPath(projectDir, path); ok {
			rec.Path = norm
			rel = norm
		}
		rec.addGrepLine(rel, line, excerpt)
		out = append(out, rec)
	}
	for _, cs := range root.Pack.CallSites {
		appendRec(cs.Path, cs.Line, cs.Excerpt)
	}
	for _, s := range root.Pack.Skeleton {
		if !summarizeSkeletonCitable(s) {
			continue
		}
		appendRec(s.Path, s.Line, summarizeSkeletonExcerpt(s, root.Pack.Substance))
	}
	if len(out) == 0 {
		task := strings.TrimSpace(root.Task)
		if task != "" {
			if path := summarizePackPrimaryPath(root.Pack); path != "" {
				appendRec(path, 1, task)
			}
		}
	}
	return out
}

func parseSummarizeWire(content string) (summarizeWireRoot, bool) {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return summarizeWireRoot{}, false
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return summarizeWireRoot{}, false
	}
	var root summarizeWireRoot
	if err := json.Unmarshal(raw, &root); err != nil {
		return summarizeWireRoot{}, false
	}
	if root.Pack.Identity == nil && root.Pack.Skeleton == nil && root.Pack.Substance == nil &&
		root.Pack.CallSites == nil && len(root.Anchors) == 0 && strings.TrimSpace(root.Task) == "" {
		return summarizeWireRoot{}, false
	}
	return root, true
}

func indexSummarizeSpan(projectDir string, rec *Record, path string, line int, excerpt string) {
	path = strings.TrimSpace(path)
	excerpt = strings.TrimSpace(excerpt)
	if path == "" {
		return
	}
	addRecordPath(projectDir, rec, path, FidelityStructured)
	if line <= 0 || excerpt == "" {
		return
	}
	rel, ok := NormalizeCitationPath(projectDir, path)
	if !ok {
		return
	}
	appendLineRange(rec, line, line)
	rec.addGrepLine(rel, line, excerpt)
}

func summarizeSkeletonCitable(s summarizePackSymbolWire) bool {
	if s.Line <= 0 {
		return false
	}
	kind := strings.TrimSpace(s.Kind)
	return kind != summarizeKindDirectoryMap && kind != summarizeKindDirectoryRollup
}

func summarizeSkeletonExcerpt(s summarizePackSymbolWire, substance []summarizePackWindowWire) string {
	excerpt := strings.TrimSpace(s.Name)
	for _, w := range substance {
		if strings.TrimSpace(w.Path) != strings.TrimSpace(s.Path) {
			continue
		}
		if s.Line < w.StartLine || (w.EndLine > 0 && s.Line > w.EndLine) {
			continue
		}
		if line := numberedWindowLine(w.Body, s.Line); line != "" {
			if cleaned := StripNumberedLinePrefix(line); summarizeExcerptUsable(cleaned) {
				return cleaned
			}
		}
		break
	}
	if summarizeExcerptUsable(excerpt) {
		return excerpt
	}
	return ""
}

func summarizePackPrimaryPath(pack summarizePackWire) string {
	for _, id := range pack.Identity {
		kind := strings.TrimSpace(id.Kind)
		if kind == "file" || kind == "inline" {
			if path := strings.TrimSpace(id.Path); path != "" {
				return path
			}
		}
	}
	for _, id := range pack.Identity {
		if path := strings.TrimSpace(id.Path); path != "" {
			return path
		}
	}
	return ""
}

type substanceLine struct {
	line    int
	content string
}

func substanceBodyLines(body string) []substanceLine {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var out []substanceLine
	for _, row := range strings.Split(body, "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		i := strings.IndexByte(row, ':')
		if i <= 0 || i > 6 {
			continue
		}
		lineStr := row[:i]
		digits := true
		for _, c := range lineStr {
			if c < '0' || c > '9' {
				digits = false
				break
			}
		}
		if !digits {
			continue
		}
		n, err := strconv.Atoi(lineStr)
		if err != nil || n <= 0 {
			continue
		}
		content := strings.TrimSpace(row[i+1:])
		if content == "" {
			continue
		}
		out = append(out, substanceLine{line: n, content: content})
	}
	return out
}

func numberedWindowLine(body string, line int) string {
	if body == "" || line <= 0 {
		return ""
	}
	prefix := strconv.Itoa(line) + ":"
	for _, row := range strings.Split(body, "\n") {
		t := strings.TrimSpace(row)
		if strings.HasPrefix(t, prefix) {
			return t
		}
	}
	return ""
}

// StripNumberedLinePrefix drops a leading "NN:" line number from an excerpt, so
// a citation body matches the source it was taken from.
func StripNumberedLinePrefix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, ':'); i > 0 && i <= 6 {
		digits := true
		for _, c := range s[:i] {
			if c < '0' || c > '9' {
				digits = false
				break
			}
		}
		if digits {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	return s
}

func summarizeExcerptUsable(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' {
			return true
		}
	}
	return false
}

// IsEvidenceHandleToken reports whether token is a host evidence handle (read#3, summarize#15).
func IsEvidenceHandleToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if p, _, ok := SplitPathLineToken(token); ok {
		token = p
	}
	return HandleGrammar.MatchString(token)
}
