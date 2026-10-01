package conditions

import (
	"strings"

	"github.com/lycaon/lycaon/internal/blueprintfile"
)

// PlanStubTitleLabel is the agent-facing requirement for a declared blueprint name.
const PlanStubTitleLabel = "`title:` in frontmatter (a name, not the host placeholder)"

// PlanStubValidText reports whether plan markdown satisfies plan_stub_valid.
func PlanStubValidText(text string) bool {
	return BlueprintMaterializedText(text) && len(PlanStubMissingRequired(text)) == 0
}

// planStubHeadings is the ## sections plan_stub_valid enforces. The first entry
// of each row is the canonical title the agent should write; the rest are titles
// evaluation also accepts.
var planStubHeadings = [][]string{
	{"## Goal"},
	{"## Assumptions"},
	{"## Plan implementation scope", "## Scope"},
	{"## Plan breaking changes", "## Breaking changes", "## Breaking"},
	{"## Approach", "## Expand", "## Expansion scope"},
}

// PlanStubRequiredLabels is everything a valid stub must carry: the canonical ##
// headings plus each structured frontmatter field, rendered the way the agent
// must write it.
func PlanStubRequiredLabels() []string {
	out := make([]string, 0, len(planStubHeadings))
	for _, row := range planStubHeadings {
		out = append(out, row[0])
	}
	out = append(out, PlanStubTitleLabel)
	fields, _ := PlanBlueprintFields()
	for _, field := range fields {
		out = append(out, field.Prompt())
	}
	return out
}

// PlanStubMissingRequired returns required stub labels that are still absent —
// a heading missing or empty, or a structured field whose frontmatter value is
// absent or outside its declared vocabulary.
func PlanStubMissingRequired(text string) []string {
	if strings.TrimSpace(text) == "" {
		return PlanStubRequiredLabels()
	}
	var missing []string
	for _, row := range planStubHeadings {
		if PlanSectionMissing(text, row...) {
			missing = append(missing, row[0])
		}
	}
	if _, ok := blueprintfile.DeclaredTitle(text); !ok {
		missing = append(missing, PlanStubTitleLabel)
	}
	fields, _ := PlanBlueprintFields()
	for _, field := range fields {
		if _, ok := PlanFieldDeclared(text, field.Key); !ok {
			missing = append(missing, field.Prompt())
		}
	}
	return missing
}

// MarkdownH2Headings returns non-empty ## headings present in markdown (ATX H2 only).
func MarkdownH2Headings(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			continue
		}
		h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		if h == "" {
			continue
		}
		out = append(out, "## "+h)
	}
	return out
}

// PlanSectionContent returns the trimmed body under a heading.
func PlanSectionContent(text, heading string) string {
	idx := strings.Index(text, heading)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(heading):]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		rest = rest[nl+1:]
	}
	end := len(rest)
	for _, h := range []string{"\n## ", "\n# "} {
		if i := strings.Index(rest, h); i >= 0 && i < end {
			end = i
		}
	}
	return strings.TrimSpace(rest[:end])
}

// PlanSectionMissing reports whether every listed heading is absent or empty.
func PlanSectionMissing(text string, headings ...string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	for _, heading := range headings {
		if !strings.Contains(text, heading) {
			continue
		}
		if PlanSectionContent(text, heading) != "" {
			return false
		}
	}
	return true
}

// ResearchRequiredText is true when a valid stub declares a depth that opens research.
func ResearchRequiredText(text string) bool {
	if !PlanStubValidText(text) {
		return false
	}
	declared, ok := PlanFieldDeclared(text, ResearchDepthFieldKey)
	if !ok {
		return true
	}
	return declared.OpensResearch
}
