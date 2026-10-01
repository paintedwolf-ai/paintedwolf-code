package extpacks

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/skills"
)

// Malformed skills emit diagnostics without blocking valid skills.
func LoadEffectiveSkills(eff *EffectiveCatalog) ([]skills.Skill, []Diagnostic) {
	if eff == nil {
		return nil, nil
	}
	var out []skills.Skill
	var diags []Diagnostic
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, "skills/") {
			continue
		}
		u, ok := eff.Loaded[id]
		if !ok {
			continue
		}
		dirName := strings.TrimPrefix(id, "skills/")
		res := skills.Parse(dirName, u.Content)
		for _, note := range res.Notes {
			diags = append(diags, Diagnostic{
				Code:    skillNoteDiag(note),
				Message: skillNoteMessage(note),
				UnitID:  id,
				PackID:  u.WinnerPackID,
			})
		}
		if res.Skill.Name == "" {
			continue
		}
		sk := res.Skill
		// A pack body is a template; an unparsable one otherwise fails at
		// activation, mid-turn.
		if !eff.StockAuthority(u.WinnerPackID) {
			if _, err := pongoplain.Compile(sk.Body); err != nil {
				diags = append(diags, Diagnostic{
					Code:    DiagSkillTemplateInvalid,
					Message: fmt.Sprintf("skill %q is not a valid template, so reading it fails at activation: %v", sk.Name, err),
					UnitID:  id,
					PackID:  u.WinnerPackID,
				})
				continue
			}
		}
		sk.UnitID = id
		sk.PackID = u.WinnerPackID
		sk.UserProvided = !eff.StockAuthority(u.WinnerPackID)
		if at, ok := eff.UnitPath(id); ok {
			dir := at.Parent()
			sk.Dir = dir.String()
			fsys, root := dir.FS()
			sk.BindResources(fsys, root, skills.DiscoverBundledResources(fsys, root), "")
		}
		out = append(out, sk)
	}
	if len(out) > skills.CatalogMax {
		out = out[:skills.CatalogMax]
		diags = append(diags, Diagnostic{
			Code:    DiagSkillCatalogFull,
			Message: skillNoteMessage(skills.NoteCatalogFull),
		})
	}
	return out, diags
}

func skillNoteDiag(note string) string {
	switch note {
	case skills.NoteNameInvalid:
		return DiagSkillNameInvalid
	case skills.NoteNameMismatch:
		return DiagSkillNameMismatch
	case skills.NoteFrontmatterInvalid:
		return DiagSkillFrontmatterInvalid
	case skills.NoteFieldMissing:
		return DiagSkillFieldMissing
	case skills.NoteFieldInvalid:
		return DiagSkillFieldInvalid
	case skills.NoteCompatibilityInvalid:
		return DiagSkillCompatibilityInvalid
	case skills.NoteTooLarge:
		return DiagSkillTooLarge
	case skills.NoteCatalogFull:
		return DiagSkillCatalogFull
	case skills.NoteShadowed:
		return DiagSkillShadowed
	default:
		return note
	}
}

func skillNoteMessage(note string) string {
	switch note {
	case skills.NoteNameInvalid:
		return "Skill folder names use lowercase letters, numbers and single hyphens."
	case skills.NoteNameMismatch:
		return "The name in SKILL.md must match its folder."
	case skills.NoteFrontmatterInvalid:
		return "SKILL.md has no readable frontmatter block."
	case skills.NoteFieldMissing:
		return "SKILL.md needs a description."
	case skills.NoteFieldInvalid:
		return "The description is over the 1024-character limit."
	case skills.NoteCompatibilityInvalid:
		return "Compatibility is over the 500-character limit."
	case skills.NoteTooLarge:
		return "SKILL.md is over the 64 KiB limit and was not loaded."
	case skills.NoteCatalogFull:
		return "The skills catalog is full at 128; skills beyond the cap were not loaded."
	case skills.NoteShadowed:
		return "A skill with this name is already available, so this copy is not used."
	default:
		return note
	}
}

// diagnosticsFromProjectNotes maps skills package notes to extension diagnostics.
func diagnosticsFromProjectNotes(notes []skills.ProjectNote) []Diagnostic {
	if len(notes) == 0 {
		return nil
	}
	out := make([]Diagnostic, 0, len(notes))
	for _, n := range notes {
		out = append(out, Diagnostic{
			Code:    skillNoteDiag(n.Note),
			Message: skillNoteMessage(n.Note),
			UnitID:  n.UnitID,
		})
	}
	return out
}
