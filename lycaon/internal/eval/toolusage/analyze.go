package toolusage

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/logview"
)

var (
	reMatchLines  = regexp.MustCompile(`(?m)^\s*\d+:`)
	reResultCount = regexp.MustCompile(`"count"\s*:\s*(\d+)`)
)

type readArgs struct {
	Path   string `json:"path"`
	Range  string `json:"range"`
	Symbol string `json:"symbol"`
	Offset *int   `json:"offset"`
	Full   bool   `json:"full"`
	Mode   string `json:"mode"`
}

// AnalyzeCapture builds a profile from decoded capture rows.
func AnalyzeCapture(mode, captureDir string, sessions []logview.SessionRecord, llm []logview.LLMRecord) Profile {
	tree := logview.BuildSessionTree(sessions, llm)
	p := Profile{
		SchemaVersion:        SchemaVersion,
		Mode:                 mode,
		CaptureDir:           captureDir,
		Runs:                 1,
		ToolCalls:            map[string]int{},
		ToolCallsByAgentType: map[string]map[string]int{},
		ToolCallsBySurface:   map[string]map[string]int{},
	}
	p.Model, p.ProviderID = coordinatorModel(tree)
	pathReads := map[string]int{}
	for _, agent := range tree.Agents {
		agentType := orDash(agent.AgentType)
		surface := orDash(agent.Surface)
		if agent.IsCoordinator() {
			p.TaskSuccess.CoordinatorSeen = true
		}
		events := agent.ToolEvents()
		analyzeSurveySequence(&p.Survey, events)
		analyzeVisualCalls(&p.Visual, events)
		for _, event := range events {
			name := strings.TrimSpace(event.Name)
			if name == "" {
				continue
			}
			p.ToolCalls[name]++
			incNested(p.ToolCallsByAgentType, agentType, name)
			incNested(p.ToolCallsBySurface, surface, name)
			switch name {
			case "read":
				p.Read.TotalReads++
				path, whole := classifyRead(event.Args)
				if whole {
					p.Read.WholeFileReads++
				} else {
					p.Read.ScopedReads++
				}
				if path != "" {
					pathReads[path]++
				}
			case "grep":
				p.Search.GrepCalls++
				if n := cardinalityFromResult(event.Result, "grep"); n >= 0 {
					p.Search.GrepMatchCardinality = append(p.Search.GrepMatchCardinality, n)
				}
			case "find":
				p.Search.FindCalls++
				if n := cardinalityFromResult(event.Result, "find"); n >= 0 {
					p.Search.FindResultCardinality = append(p.Search.FindResultCardinality, n)
				}
			case "glob":
				p.Search.GlobCalls++
				if n := cardinalityFromResult(event.Result, "glob"); n >= 0 {
					p.Search.FindResultCardinality = append(p.Search.FindResultCardinality, n)
				}
			}
		}
		for _, turn := range agent.Turns {
			if turn.Usage != nil {
				p.TokenSpend.PromptTokens += turn.Usage.PromptTokens
				p.TokenSpend.CompletionTokens += turn.Usage.CompletionTokens
				p.Cache.CacheReadInputTokens += turn.Usage.CacheReadInputTokens
				p.Cache.CacheCreationInputTokens += turn.Usage.CacheCreationInputTokens
			} else {
				p.TokenSpend.MissingUsageCalls++
			}
		}
		switch agent.Outcome.Status {
		case logview.StatusComplete:
			if !agent.IsCoordinator() {
				p.TaskSuccess.WorkersComplete++
			}
		case logview.StatusFailed:
			if !agent.IsCoordinator() {
				p.TaskSuccess.WorkersFailed++
			}
		case logview.StatusPartial:
			if !agent.IsCoordinator() {
				p.TaskSuccess.WorkersPartial++
			}
		case logview.StatusUnknown, logview.StatusBlocked, logview.StatusRunning:
		}
	}
	p.TaskSuccess.AgentsTotal = len(tree.Agents)
	for _, count := range pathReads {
		if count > 1 {
			p.Read.ReReadPaths++
		}
	}
	if p.Read.TotalReads > 0 {
		p.Read.WholeFileRatio = float64(p.Read.WholeFileReads) / float64(p.Read.TotalReads)
	}
	if len(pathReads) > 0 {
		p.Read.ReReadRatio = float64(p.Read.ReReadPaths) / float64(len(pathReads))
	}
	p.Cache.HitRate = cacheHitRate(p.TokenSpend.PromptTokens, p.Cache.CacheReadInputTokens)
	return normalizeProfile(p)
}

// Prefer an agent-surface call over auxiliary calls attached to its session.
func coordinatorModel(tree *logview.SessionTree) (string, string) {
	var fallback *logview.LLMRecord
	for _, agent := range tree.Agents {
		if !agent.IsCoordinator() {
			continue
		}
		for i := range agent.Turns {
			turn := &agent.Turns[i]
			if turn.Model == "" {
				continue
			}
			if turn.Surface != "" {
				return turn.Model, turn.ProviderID
			}
			if fallback == nil {
				fallback = turn
			}
		}
	}
	if fallback != nil {
		return fallback.Model, fallback.ProviderID
	}
	return "", ""
}

func analyzeSurveySequence(metrics *SurveyMetrics, events []logview.ToolEvent) {
	if metrics == nil || len(events) == 0 {
		return
	}
	type batchState struct {
		summaries int
		mixed     bool
	}
	batches := map[int]*batchState{}
	scopes := map[string]int{}
	for i, event := range events {
		name := strings.TrimSpace(event.Name)
		if name == "summarize" {
			metrics.SummarizeCalls++
			state := batches[event.Batch]
			if state == nil {
				state = &batchState{}
				batches[event.Batch] = state
			}
			state.summaries++
			if scope := toolPath(event.Args); scope != "" {
				scopes[scope]++
			}
			if i > 0 && strings.TrimSpace(events[i-1].Name) == "summarize" {
				metrics.ConsecutiveSummarizeCalls++
			}
			if i+1 < len(events) && isTargetedInspection(events[i+1].Name) {
				metrics.TargetedInspectionsAfterSummary++
			}
		}
	}
	for _, event := range events {
		state := batches[event.Batch]
		if state == nil || strings.TrimSpace(event.Name) == "summarize" {
			continue
		}
		if isSurveyTool(event.Name) {
			state.mixed = true
		}
	}
	for _, state := range batches {
		metrics.SummarizeBatches++
		if state.summaries > metrics.MaxSummariesInBatch {
			metrics.MaxSummariesInBatch = state.summaries
		}
		if state.mixed {
			metrics.MixedSurveyBatches++
		}
	}
	for _, count := range scopes {
		if count > 1 {
			metrics.RepeatedSummarizeScopes += count - 1
		}
	}
}

func isSurveyTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "list_dir", "find", "grep", "read", "survey_repo":
		return true
	default:
		return false
	}
}

func isTargetedInspection(name string) bool {
	switch strings.TrimSpace(name) {
	case "grep", "read":
		return true
	default:
		return false
	}
}

func toolPath(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var value struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value.Path)
}

func classifyRead(args json.RawMessage) (path string, wholeFile bool) {
	if len(args) == 0 {
		return "", true
	}
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", true
	}
	path = strings.TrimSpace(a.Path)
	scoped := a.Range != "" || a.Symbol != "" || a.Offset != nil || a.Full || strings.EqualFold(a.Mode, "content")
	return path, !scoped
}

func cardinalityFromResult(result, tool string) int {
	if strings.TrimSpace(result) == "" {
		return -1
	}
	if m := reResultCount.FindStringSubmatch(result); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	switch tool {
	case "grep":
		return len(reMatchLines.FindAllStringIndex(result, -1))
	case "find", "glob":
		return strings.Count(result, "\n") + 1
	default:
		return -1
	}
}

func cacheHitRate(prompt, cacheRead int) float64 {
	if prompt <= 0 {
		return 0
	}
	// Captures normalize input tokens inclusively; cache hits are a subset.
	return float64(min(prompt, max(0, cacheRead))) / float64(prompt)
}

func incNested(m map[string]map[string]int, outer, inner string) {
	if m[outer] == nil {
		m[outer] = map[string]int{}
	}
	m[outer][inner]++
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown)"
	}
	return s
}
