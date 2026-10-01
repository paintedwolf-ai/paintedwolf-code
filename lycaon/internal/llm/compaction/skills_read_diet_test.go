package compaction_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSkillsReadDietPreservesSuccessfulResult(t *testing.T) {
	if got := evidence.ActiveBinding().TranscriptDietForTool("skills_read"); got != evidence.TranscriptDietPreserveStructure {
		t.Fatalf("skills_read binding diet = %q want preserve_structure", got)
	}
	body := "# Skill body\n\nDo the durable thing.\n\nSkill directory: /tmp/skills/demo\n"
	msgs := []compaction.ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "go"},
		{Role: string(api.MessageRoleAssistant), Content: "ok"},
		{Role: string(api.MessageRoleTool), ToolName: "skills_read", Content: body},
		{Role: string(api.MessageRoleAssistant), Content: "next"},
	}
	class := compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
		Index:    2,
		Messages: msgs,
		Binding:  evidence.ActiveBinding(),
	})
	if class.Strategy != evidence.TranscriptDietPreserveStructure {
		t.Fatalf("strategy = %q want preserve_structure", class.Strategy)
	}
}
