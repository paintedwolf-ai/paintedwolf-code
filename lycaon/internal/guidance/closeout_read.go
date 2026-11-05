package guidance

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/jsonfence"
	"github.com/lycaon/lycaon/internal/jsonshape"
)

// A closeout is Markdown. On a workflow report phase the final top-level json
// fence is the report document; everywhere else a fence is a literal example.

// AnswerInFence marks a fence that carries the answer itself as `synthesis`;
// the answer is the Markdown above the fence.
const AnswerInFence jsonshape.Kind = "answer_in_fence"

// RetiredFenceMember marks a top-level member that now has inline syntax.
const RetiredFenceMember jsonshape.Kind = "retired_fence_member"

// reportFenceInvalid marks a fence whose body is not one JSON object.
const reportFenceInvalid jsonshape.Kind = "not_json_object"

// CloseoutRead is one model response read as a closeout, applied to the retained
// draft when a repair cycle is active.
type CloseoutRead struct {
	Draft CloseoutDraft
	// Unread are the document members the report did not take.
	Unread []jsonshape.Issue
	// RepairDefects refuse this response's repair. Draft is then the retained
	// draft, untouched.
	RepairDefects []RepairDefect
}

// ReadCloseoutReport reads a model response's screened Markdown. expectsRunReport
// is set only on the phase that delivers a workflow's report.
func ReadCloseoutReport(raw string, retained RetainedCloseout, expectsRunReport bool, opts citationmd.Options) CloseoutRead {
	narrative, document, unread := raw, (*WorkflowRunReportDocument)(nil), []jsonshape.Issue(nil)
	if expectsRunReport {
		if before, fence, ok := splitReportFence(raw, opts); ok {
			narrative = before
			document, unread = readReportFence(fence)
		}
	}
	next := ParseCloseoutDraft(narrative, opts)
	if retained.Active && retained.Draft.HasAnswer() {
		read := applyRetainedRepair(next, document, retained, opts)
		read.Unread = unread
		return read
	}
	next.setDocument(document)
	return CloseoutRead{Draft: next, Unread: unread, RepairDefects: inactiveRepairDefects(next)}
}

// splitReportFence separates a closed top-level json fence that ends the
// document. A fence inside a container or with another info string stays literal.
func splitReportFence(raw string, opts citationmd.Options) (before, fence string, ok bool) {
	parsed := citationmd.Parse(raw, opts)
	if len(parsed.Zones) == 0 {
		return "", "", false
	}
	last := parsed.Zones[len(parsed.Zones)-1]
	body := raw[parsed.Body.Start:parsed.Body.End]
	markdown, block, closed := jsonfence.Trailing(body)
	if !closed || !strings.EqualFold(block.Language, "json") {
		return "", "", false
	}
	if last.Kind != citationmd.ZoneFence || !atLineStart(raw, last.Span.Start) {
		return "", "", false
	}
	return strings.TrimSpace(markdown), block.Payload, true
}

// atLineStart reports whether only a fence's permitted indent precedes it.
func atLineStart(source string, at int) bool {
	lineStart := strings.LastIndexByte(source[:at], '\n') + 1
	prefix := source[lineStart:at]
	return len(prefix) <= 3 && strings.TrimSpace(prefix) == ""
}

// readReportFence decodes the document strictly. Every member the document does
// not declare is unread, which refuses the report on the document's own budget.
func readReportFence(fence string) (*WorkflowRunReportDocument, []jsonshape.Issue) {
	var document WorkflowRunReportDocument
	unread, err := jsonshape.Decode([]byte(fence), &document)
	if err != nil {
		return nil, []jsonshape.Issue{{Path: "report fence", Pattern: "report fence", Name: "report fence", Kind: reportFenceInvalid, Want: "one JSON object"}}
	}
	for i := range unread {
		if unread[i].Parent == "" && slices.Contains(retiredFenceMembers, unread[i].Name) {
			unread[i].Kind = RetiredFenceMember
			if unread[i].Name == DraftFieldSynthesis {
				unread[i].Kind = AnswerInFence
			}
		}
	}
	document.normalize()
	return &document, unread
}

// retiredFenceMembers now live in the Markdown above the fence.
var retiredFenceMembers = []string{DraftFieldSynthesis, "cited_evidence", "cited_urls", "artifact_ids", "verification"}

// reportFenceMembers are the members a report fence may carry.
var reportFenceMembers = jsonshape.Fields(reflect.TypeFor[WorkflowRunReportDocument]())

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
	case issue.Kind == RetiredFenceMember:
		return name + ": written inline in the Markdown above the fence"
	case issue.Kind == reportFenceInvalid, issue.Kind == jsonshape.Mismatch:
		return name + ": want " + issue.Want
	case issue.Parent != "" && slices.Contains(reportFenceMembers, issue.Name):
		return name + ": `" + issue.Name + "` is a top-level report field"
	default:
		return name + ": not a report field"
	}
}

// ReportDocumentFence renders the document a repair returns whole, holding only
// document members. Empty when the host read no document.
func ReportDocumentFence(draft CloseoutDraft) string {
	if draft.Document == nil {
		return ""
	}
	raw, err := json.Marshal(draft.Document)
	if err != nil {
		return ""
	}
	return string(raw)
}
