package guidance

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonfence"
	"github.com/lycaon/lycaon/internal/jsonshape"
)

// A closeout arrives as a JSON envelope, as Markdown with one trailing json
// fence of report fields, or, during a repair, as that fence alone for the
// pinned body. Every form is read against the same report type: members it
// declares are kept, and every other member is named as unread so a refusal
// can say which one, and so a report the host stores anyway never silently
// loses what the coordinator wrote.

// AnswerInFence marks a fence that carries the answer itself as `synthesis`;
// the answer is the Markdown above the fence.
const AnswerInFence jsonshape.Kind = "answer_in_fence"

// CloseoutRead is a closeout draft read as a report.
type CloseoutRead struct {
	Report CoordinatorCompletionReport
	// Unread are the draft's JSON members the report did not take.
	Unread []jsonshape.Issue
}

// ReadCloseoutReport reads a closeout draft in any form a coordinator sends
// it. Plain Markdown is a report without fields. pinnedSynthesis is the body a
// fence-only repair belongs to. ok is false for a draft with no answer.
func ReadCloseoutReport(content, pinnedSynthesis string) (CloseoutRead, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return CloseoutRead{}, false
	}
	if read, ok := jsonfence.ParseStrict(content, decodeCloseoutEnvelope); ok {
		return read, true
	}
	if body, fence, ok := splitReportFence(content); ok {
		read := readReportFence(fence)
		switch {
		case body != "" && !CloseoutBodyIsEnvelopeShaped(body):
			read.Report.Synthesis = body
		case body == "" && strings.TrimSpace(pinnedSynthesis) != "":
			read.Report.Synthesis = pinnedSynthesis
		default:
			return CloseoutRead{}, false
		}
		read.Report.Normalize()
		return read, true
	}
	if read, ok := salvageCloseoutEnvelope(content); ok {
		return read, true
	}
	if CloseoutBodyIsEnvelopeShaped(content) {
		return CloseoutRead{}, false
	}
	return CloseoutRead{Report: CoordinatorCompletionReport{Synthesis: content}}, true
}

// ParseCoordinatorCompletionReport reads a stored closeout envelope: JSON
// with the answer as synthesis and no member the report does not declare.
func ParseCoordinatorCompletionReport(content string) (CoordinatorCompletionReport, bool) {
	read, ok := jsonfence.ParseStrict(content, decodeCloseoutEnvelope)
	if !ok || len(read.Unread) > 0 {
		return CoordinatorCompletionReport{}, false
	}
	return read.Report, true
}

// UsableCloseoutSynthesis is the answer a closeout draft carries, whatever
// its fields; empty when it carries none.
func UsableCloseoutSynthesis(drafted string) string {
	read, ok := ReadCloseoutReport(drafted, "")
	if !ok || CloseoutBodyIsEnvelopeShaped(read.Report.Synthesis) {
		return ""
	}
	return read.Report.Synthesis
}

// CloseoutBodyIsEnvelopeShaped recognizes structural JSON prefixes.
func CloseoutBodyIsEnvelopeShaped(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "```")
}

// salvageCloseoutEnvelope reads the first JSON value in malformed content
// that holds an answer, such as an envelope closed early with its fields
// trailing after it. A trailing report fence under Markdown is read first,
// so its members never stand in for the Markdown answer.
func salvageCloseoutEnvelope(content string) (CloseoutRead, bool) {
	for _, candidate := range jsonfence.Candidates(content) {
		if read, ok := decodeCloseoutEnvelope(candidate); ok {
			return read, true
		}
	}
	return CloseoutRead{}, false
}

func decodeCloseoutEnvelope(candidate string) (CloseoutRead, bool) {
	var read CloseoutRead
	unread, err := jsonshape.Decode([]byte(candidate), &read.Report)
	if err != nil {
		return CloseoutRead{}, false
	}
	read.Report.Normalize()
	if read.Report.Synthesis == "" || CloseoutBodyIsEnvelopeShaped(read.Report.Synthesis) {
		return CloseoutRead{}, false
	}
	read.Unread = unread
	return read, true
}

// readReportFence reads a trailing fence's report fields. The answer is the
// Markdown above it, so a synthesis inside it is unread.
func readReportFence(fence string) CloseoutRead {
	var read CloseoutRead
	read.Unread, _ = jsonshape.Decode([]byte(fence), &read.Report)
	if strings.TrimSpace(read.Report.Synthesis) != "" {
		read.Unread = append(read.Unread, jsonshape.Issue{Path: "synthesis", Pattern: "synthesis", Name: "synthesis", Kind: AnswerInFence})
	}
	read.Report.Synthesis = ""
	return read
}

// reportFenceMembers are the members a report fence may carry.
var reportFenceMembers = jsonshape.Fields(reflect.TypeFor[CoordinatorCompletionReport]())

// splitReportFence separates a closeout's Markdown from its trailing json
// fence. A fence is the report's only when it is a JSON object with at least
// one report member; any other trailing block is part of the answer.
func splitReportFence(content string) (body, fence string, ok bool) {
	const marker = "```"
	if !strings.HasSuffix(content, marker) {
		return "", "", false
	}
	inner := content[:len(content)-len(marker)]
	open := strings.LastIndex(inner, marker)
	if open < 0 {
		return "", "", false
	}
	info, raw, found := strings.Cut(inner[open+len(marker):], "\n")
	if !found {
		return "", "", false
	}
	if info = strings.TrimSpace(info); info != "json" && info != "json closeout" {
		return "", "", false
	}
	var members map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &members) != nil {
		return "", "", false
	}
	for name := range members {
		if slices.Contains(reportFenceMembers, name) {
			return strings.TrimSpace(inner[:open]), raw, true
		}
	}
	return "", "", false
}

// UnreadReportFields sample the unread members for a refusal, one entry per
// member pattern with how often it occurred, and the total number of entries.
func UnreadReportFields(unread []jsonshape.Issue) (sample []string, total int) {
	type group struct {
		issue jsonshape.Issue
		count int
	}
	var groups []*group
	byPattern := map[string]*group{}
	for _, issue := range unread {
		key := issue.Pattern + "\x00" + string(issue.Kind)
		g, ok := byPattern[key]
		if !ok {
			g = &group{issue: issue}
			byPattern[key] = g
			groups = append(groups, g)
		}
		g.count++
	}
	for _, g := range groups {
		sample = append(sample, unreadField(g.issue, g.count))
	}
	return sample, len(groups)
}

// unreadField names one unread member and what the report reads instead.
func unreadField(issue jsonshape.Issue, count int) string {
	name := "`" + issue.Pattern + "`"
	if count > 1 {
		name += fmt.Sprintf(" (%d)", count)
	}
	switch {
	case issue.Kind == AnswerInFence:
		return name + ": the answer is the Markdown above the fence"
	case issue.Kind == jsonshape.Mismatch:
		return name + ": want " + issue.Want
	case issue.Parent != "" && slices.Contains(reportFenceMembers, issue.Name):
		return name + ": `" + issue.Name + "` is a top-level report field"
	default:
		return name + ": not a report field"
	}
}
