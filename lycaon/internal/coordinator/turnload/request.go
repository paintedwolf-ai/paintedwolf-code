package turnload

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
)

// RequestOutcome is what one request_tools call established.
type RequestOutcome struct {
	// Need is the text the model wrote.
	Need string `json:"need"`
	// Exact names loadable tools the text named outright.
	Exact []string `json:"exact,omitempty"`
	// Ranked maps each tool the engine chose to its relevance score.
	Ranked map[string]float64 `json:"ranked,omitempty"`
	// Nearest maps the engine's closest tools to their scores when it
	// answered and none reached the load threshold. They load anyway and
	// the result names them as approximate.
	Nearest map[string]float64 `json:"nearest,omitempty"`
	Engine  decide.Engine      `json:"engine"`
	// Abstained is true when the engine did not answer; Reason says why.
	Abstained bool           `json:"abstained"`
	Reason    string         `json:"reason,omitempty"`
	Failure   RankingFailure `json:"failure,omitempty"`
}

// Loaded returns every tool the request resolved to: exact names first,
// then the engine's ranking best first, then its nearest tools.
func (o RequestOutcome) Loaded() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, name := range o.Exact {
		add(name)
	}
	for _, name := range byScore(o.Ranked) {
		add(name)
	}
	for _, name := range byScore(o.Nearest) {
		add(name)
	}
	return out
}

// byScore orders scored names best first, ties by name.
func byScore(scores map[string]float64) []string {
	names := make([]string, 0, len(scores))
	for name := range scores {
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		if scores[names[i]] != scores[names[j]] {
			return scores[names[i]] > scores[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

// ExactNames returns the loadable tool names the text spells out.
func ExactNames(need string, cards []ToolCard) []string {
	names := make([]string, 0, len(cards))
	for _, card := range cards {
		names = append(names, card.Name)
	}
	return exactCatalogNames(need, names)
}

func exactCatalogNames(need string, names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name != "" && !seen[name] && containsCatalogID(need, name) {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func containsCatalogID(text, name string) bool {
	text, name = strings.ToLower(text), strings.ToLower(name)
	for start := 0; start < len(text); {
		i := strings.Index(text[start:], name)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(name)
		boundary := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-' }
		if (i == 0 || !boundary(text[i-1])) && (end == len(text) || !boundary(text[end])) {
			return true
		}
		start = i + 1
	}
	return false
}

// ResolveRequest turns request_tools text into the schemas to load: names
// the text spells out, then the engine's ranking of the rest or its nearest
// tools when none reaches the bar.
func ResolveRequest(ctx context.Context, d decide.Decider, spec RequestSpec, need string, cards []ToolCard) RequestOutcome {
	need = strings.TrimSpace(need)
	out := RequestOutcome{Need: need, Exact: ExactNames(need, cards)}
	if len(out.Exact) == 1 && strings.EqualFold(need, out.Exact[0]) {
		return out
	}
	rest := make([]ToolCard, 0, len(cards))
	exact := make(map[string]bool, len(out.Exact))
	for _, name := range out.Exact {
		exact[name] = true
	}
	for _, card := range cards {
		if !exact[strings.TrimSpace(card.Name)] {
			rest = append(rest, card)
		}
	}
	if need == "" || len(rest) == 0 {
		out.Abstained, out.Reason = true, "nothing to rank"
		return out
	}
	if d == nil || !d.Available() {
		out.Abstained, out.Reason, out.Failure = true, "engine unavailable", RankingUnavailable
		return out
	}
	callCtx, cancel := context.WithTimeout(ctx, spec.Deadline())
	defer cancel()
	candidates := make([]string, len(rest))
	for i, card := range rest {
		candidates[i] = RequestCandidate(card)
	}
	scores, engine, err := d.Rank(callCtx, decide.HeadUnitRank, need, candidates)
	out.Engine = engine
	if err == nil {
		err = callCtx.Err()
	}
	if err == nil {
		err = validateRanking(scores, len(rest))
	}
	if err != nil {
		out.Abstained, out.Reason, out.Failure = true, abstainReason(err), rankingFailure(err)
		return out
	}
	all := make(map[string]float64, len(rest))
	for i, card := range rest {
		all[strings.TrimSpace(card.Name)] = scores[i]
	}
	order := byScore(all)
	out.Ranked = map[string]float64{}
	for _, name := range order {
		if all[name] < spec.LoadAt || len(out.Ranked) == spec.MaxLoads {
			break
		}
		out.Ranked[name] = all[name]
	}
	if len(out.Ranked) == 0 && spec.NearestLoads > 0 {
		out.Nearest = make(map[string]float64, spec.NearestLoads)
		for _, name := range order[:min(spec.NearestLoads, len(order))] {
			out.Nearest[name] = all[name]
		}
	}
	return out
}

// RequestCandidate is the text the engine scores for one tool against a
// request_tools need.
func RequestCandidate(card ToolCard) string {
	return "Tool " + strings.TrimSpace(card.Name) + ": " + BoundDescription(card.Description)
}
