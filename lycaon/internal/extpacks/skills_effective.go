package extpacks

import "github.com/lycaon/lycaon/internal/skills"

// CombineSkills merges device skills with approved project discoveries.
func CombineSkills(device []skills.Skill, deviceDiags []Diagnostic, project []skills.Skill, projectNotes []skills.ProjectNote) ([]skills.Skill, []Diagnostic) {
	merged, mergeNotes := skills.MergeAdditive(device, project)
	diags := append([]Diagnostic{}, deviceDiags...)
	diags = append(diags, diagnosticsFromProjectNotes(projectNotes)...)
	diags = append(diags, diagnosticsFromProjectNotes(mergeNotes)...)
	return merged, diags
}
