package turnload

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/skills"
)

// SkillRank is one available skill's relevance to a requested need.
type SkillRank struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// Ranking is the scored skill catalog for one lookup.
type Ranking struct {
	Ranks   []SkillRank    `json:"ranks,omitempty"`
	Engine  decide.Engine  `json:"engine"`
	Skipped string         `json:"skipped,omitempty"`
	Failure RankingFailure `json:"failure,omitempty"`
}

// Top returns the best-scored skill, or false when the catalog was not ranked.
func (r Ranking) Top() (SkillRank, bool) {
	if len(r.Ranks) == 0 {
		return SkillRank{}, false
	}
	return r.Ranks[0], true
}

// Select returns the best skill when it is a clear choice: its score reaches
// at, stays on the rubric, and leads the runner-up by margin. A close second
// means the text fits more than one procedure, and reading one of them would
// steer the turn on a coin flip.
func (r Ranking) Select(at, margin float64) (SkillRank, bool) {
	top, ok := r.Top()
	if !ok || at <= 0 || !(top.Score >= at && top.Score <= RankScoreCeiling) {
		return SkillRank{}, false
	}
	if len(r.Ranks) > 1 && top.Score-r.Ranks[1].Score < margin {
		return SkillRank{}, false
	}
	return top, true
}

// RankSkills scores available skills against task within deadline. A faulted
// or absent engine returns an empty ranking with the reason.
func RankSkills(ctx context.Context, d decide.Decider, deadline time.Duration, task string, roster []skills.Skill) Ranking {
	task = strings.TrimSpace(task)
	if d == nil || !d.Available() {
		return Ranking{Skipped: "engine unavailable", Failure: RankingUnavailable}
	}
	if task == "" || len(roster) == 0 {
		return Ranking{Skipped: "nothing to rank"}
	}
	candidates := make([]string, len(roster))
	for i, sk := range roster {
		candidates[i] = SkillCandidate(sk)
	}
	callCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	scores, engine, err := d.Rank(callCtx, decide.HeadUnitRank, task, candidates)
	if err == nil {
		err = callCtx.Err()
	}
	if err == nil {
		err = validateRanking(scores, len(roster))
	}
	if err != nil {
		return Ranking{Skipped: abstainReason(err), Failure: rankingFailure(err)}
	}
	ranks := make([]SkillRank, len(roster))
	for i, sk := range roster {
		ranks[i] = SkillRank{Name: sk.Name, Score: scores[i]}
	}
	sort.SliceStable(ranks, func(i, j int) bool { return ranks[i].Score > ranks[j].Score })
	return Ranking{Ranks: ranks, Engine: engine}
}

// SkillCandidate is the text the engine scores for one skill.
func SkillCandidate(sk skills.Skill) string {
	var b strings.Builder
	b.WriteString("Skill: ")
	b.WriteString(sk.Name)
	if desc := strings.TrimSpace(sk.Description); desc != "" {
		b.WriteString("\n")
		b.WriteString(desc)
	}
	return b.String()
}

// LookupOutcome is what a skills_read lookup established for one need.
type LookupOutcome struct {
	Need string `json:"need"`
	// Exact is a skill identifier the text named outright.
	Exact string `json:"exact,omitempty"`
	// Ranked is the engine's best match when it cleared read_at.
	Ranked    *SkillRank     `json:"ranked,omitempty"`
	Engine    decide.Engine  `json:"engine"`
	Abstained bool           `json:"abstained"`
	Reason    string         `json:"reason,omitempty"`
	Failure   RankingFailure `json:"failure,omitempty"`
}

// Names returns the matched skill names, best first.
func (o LookupOutcome) Names() []string {
	if o.Exact != "" {
		return []string{o.Exact}
	}
	if o.Ranked != nil {
		return []string{o.Ranked.Name}
	}
	return nil
}

// ExactSkill finds a catalog identifier named in the request. More than one
// named identifier is ambiguous and leaves selection to the ranker.
func ExactSkill(need string, roster []skills.Skill) string {
	names := make([]string, 0, len(roster))
	for _, sk := range roster {
		names = append(names, sk.Name)
	}
	matches := exactCatalogNames(need, names)
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// LookupSkills resolves one skill from free text. A named identifier wins;
// otherwise the engine's best relevant match wins.
func LookupSkills(ctx context.Context, d decide.Decider, spec LookupSpec, need string, roster []skills.Skill) LookupOutcome {
	need = strings.TrimSpace(need)
	out := LookupOutcome{Need: need}
	if need == "" || len(roster) == 0 {
		out.Abstained, out.Reason = true, "nothing to rank"
		return out
	}
	if name := ExactSkill(need, roster); name != "" {
		out.Exact = name
		return out
	}
	ranking := RankSkills(ctx, d, spec.Deadline(), need, roster)
	if ranking.Skipped != "" {
		out.Abstained, out.Reason, out.Failure = true, ranking.Skipped, ranking.Failure
		return out
	}
	out.Engine = ranking.Engine
	if top, ok := ranking.Select(spec.ReadAt, spec.Margin); ok {
		out.Ranked = &top
	}
	return out
}
