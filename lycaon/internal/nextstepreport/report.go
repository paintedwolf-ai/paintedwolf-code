// Package nextstepreport reads stored transcripts for how offered next steps
// fared. It uses tool identity and rejection codes only, never prose.
package nextstepreport

import (
	"encoding/json"
	"sort"

	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

// followWindow is how many later tool results can still answer an offer.
const followWindow = 3

// Refusal is an offered call the reader made and the host refused.
type Refusal struct {
	OfferedBy string `json:"offered_by"`
	Tool      string `json:"tool"`
	Code      string `json:"code"`
	MessageID string `json:"message_id"`
}

// Report counts what happened to offered calls in one or more transcripts.
type Report struct {
	Offered  int            `json:"offered"`
	Followed int            `json:"followed"`
	Refused  []Refusal      `json:"refused"`
	ByCode   map[string]int `json:"offers_by_code"`
}

// Add folds one session's messages, in order, into the report.
func (r *Report) Add(messages []api.Message) {
	if r.ByCode == nil {
		r.ByCode = map[string]int{}
	}
	results := make([]api.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == api.MessageRoleTool && message.ToolResult != nil {
			results = append(results, message)
		}
	}
	for i, message := range results {
		for _, feedback := range message.ToolResult.Feedback {
			for _, call := range offeredCalls(feedback) {
				r.Offered++
				r.ByCode[feedback.Code]++
				r.answer(feedback.Code, call, results[i+1:min(i+1+followWindow, len(results))])
			}
		}
	}
	sort.Slice(r.Refused, func(a, b int) bool { return r.Refused[a].MessageID < r.Refused[b].MessageID })
}

func (r *Report) answer(offeredBy string, call toolreach.Call, later []api.Message) {
	for _, message := range later {
		result := message.ToolResult
		if result.Tool != call.Tool {
			continue
		}
		r.Followed++
		if result.Outcome == api.ToolResultOutcomeRejected {
			refusal := Refusal{OfferedBy: offeredBy, Tool: call.Tool, MessageID: message.ID}
			if len(result.Feedback) > 0 {
				refusal.Code = result.Feedback[0].Code
			}
			r.Refused = append(r.Refused, refusal)
		}
		return
	}
}

func offeredCalls(feedback api.ToolFeedback) []toolreach.Call {
	raw, present := feedback.Details[api.ToolFeedbackNextCalls]
	if !present {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var calls []toolreach.Call
	if json.Unmarshal(encoded, &calls) != nil {
		return nil
	}
	return calls
}
