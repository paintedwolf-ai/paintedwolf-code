package turnload

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/skills"
)

// ToolEventOutcome is what the first call of a loadable tool established
// about the turn's procedure: the roster ranked against the request and the
// tool, and the skill read or pointed to, if any.
type ToolEventOutcome struct {
	// Tool is the loadable tool whose first call asked.
	Tool string `json:"tool"`
	// Need is the text the roster was ranked against.
	Need    string  `json:"need"`
	Ranking Ranking `json:"ranking"`
	// Read is the skill whose body the turn now carries.
	Read *SkillRank `json:"read,omitempty"`
	// Pointer is the skill the turn is told about in one line, when the
	// ranking fits but not confidently enough to read it unasked.
	Pointer   *SkillRank    `json:"pointer,omitempty"`
	Engine    decide.Engine `json:"engine"`
	Abstained bool          `json:"abstained"`
	Reason    string        `json:"reason,omitempty"`
}

// Names returns the skill the event selected, read or pointed to.
func (o ToolEventOutcome) Names() []string {
	switch {
	case o.Read != nil:
		return []string{o.Read.Name}
	case o.Pointer != nil:
		return []string{o.Pointer.Name}
	}
	return nil
}

// ToolEventNeed is the text a tool event ranks the roster against: the
// request, then the tool and its bounded description.
func ToolEventNeed(request string, tool ToolCard) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(request))
	b.WriteString("\n\nFirst tool called: ")
	b.WriteString(strings.TrimSpace(tool.Name))
	if desc := BoundDescription(tool.Description); desc != "" {
		b.WriteString(" — ")
		b.WriteString(desc)
	}
	return b.String()
}

// RankToolEvent resolves the skill a turn's first loadable tool call selects:
// read at read_at, pointed to at pointer_at, each leading by margin.
func RankToolEvent(ctx context.Context, d decide.Decider, spec ToolEventSpec, request string, tool ToolCard, roster []skills.Skill) ToolEventOutcome {
	out := ToolEventOutcome{Tool: strings.TrimSpace(tool.Name), Need: ToolEventNeed(request, tool)}
	if strings.TrimSpace(request) == "" || len(roster) == 0 {
		out.Abstained, out.Reason = true, "nothing to rank"
		return out
	}
	ranking := RankSkills(ctx, d, spec.Deadline(), out.Need, roster)
	if ranking.Skipped != "" {
		out.Abstained, out.Reason = true, ranking.Skipped
		return out
	}
	out.Ranking, out.Engine = ranking, ranking.Engine
	if top, ok := ranking.Select(spec.ReadAt, spec.Margin); ok {
		out.Read = &top
		return out
	}
	if top, ok := ranking.Select(spec.PointerAt, spec.Margin); ok {
		out.Pointer = &top
	}
	return out
}
