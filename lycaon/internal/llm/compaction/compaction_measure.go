package compaction

import (
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// compactionMeasure counts the projected text, including provenance markers,
// continuation wrappers, and tool arguments. Transport framing is provider-owned.
func compactionMeasure(messages []ContextMessage, counter tokenest.Counter) (int, error) {
	projected := transcript.Project(ContextMessagesToAPI(messages, nil))
	total := 0
	for _, msg := range projected {
		n, err := counter.Count(msg.Content)
		if err != nil {
			return 0, err
		}
		total += n
		for _, call := range msg.ToolCalls {
			raw, err := json.Marshal(call.Args)
			if err != nil {
				return 0, err
			}
			n, err := counter.Count(call.Name)
			if err != nil {
				return 0, err
			}
			total += n
			n, err = counter.Count(string(raw))
			if err != nil {
				return 0, err
			}
			total += n
		}
	}
	return total, nil
}

func compactionCounter(info SessionInfo) (tokenest.Counter, error) {
	return tokenest.NewCounter(modelinfo.DefaultModelContextWindows().TextEncoding(info.Model))
}

type compactionMeasurement struct {
	Before, After int
	Method        string
}

// Both sides always use the same units, including a bounded-computation fallback.
func measureCompactionPair(info SessionInfo, before, after []ContextMessage) (compactionMeasurement, error) {
	counter, err := compactionCounter(info)
	if err != nil {
		return compactionMeasurement{}, err
	}
	result, err := measureCompactionWith(counter, before, after)
	if errors.Is(err, tokenest.ErrTextLimit) {
		return measureCompactionWith(tokenest.Counter{}, before, after)
	}
	return result, err
}

func measureCompactionWith(counter tokenest.Counter, before, after []ContextMessage) (compactionMeasurement, error) {
	result := compactionMeasurement{Method: counter.Method()}
	var err error
	result.Before, err = compactionMeasure(before, counter)
	if err != nil {
		return result, err
	}
	result.After, err = compactionMeasure(after, counter)
	return result, err
}

// usefulCompaction requires headroom in both the shared budgeting proxy and
// projected text units. Unknown tokenizers use a larger estimate margin.
func (c *SimpleCompactor) usefulCompaction(info SessionInfo, before, after []ContextMessage, minimum int) (bool, error) {
	measurement, err := measureCompactionPair(info, before, after)
	if err != nil {
		return false, err
	}
	pct := c.cfg.MinSavingsPct
	if measurement.Method == "estimated" {
		pct = max(pct, 25)
	}
	oldEstimate, newEstimate := EstimateMessagesTokens(before), EstimateMessagesTokens(after)
	return oldEstimate-newEstimate >= max(minimum, (oldEstimate*pct+99)/100) &&
		measurement.Before-measurement.After >= max(minimum, (measurement.Before*pct+99)/100), nil
}
