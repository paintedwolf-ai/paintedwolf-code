package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/protectedpath"
)

// ProjectSkillDirs returns the project-scoped skills directories in precedence
// order; the agent-policy floor protects the same list.
func ProjectSkillDirs() []string {
	dirs := protectedpath.ProjectSkillDirs()
	for i, dir := range dirs {
		dirs[i] = filepath.FromSlash(dir)
	}
	return dirs
}

// NoteShadowed marks a lower-precedence project skill.
const NoteShadowed = "shadowed"

// NoteCatalogFull marks that the project scan stopped at CatalogMax.
const NoteCatalogFull = "catalog_full"

// ProjectNote is a discovery or merge note about a project skill path.
type ProjectNote struct {
	Name   string
	UnitID string
	Note   string
	Source string // absolute file the note is about
}

// DiscoverProject loads project skills in precedence order.
func DiscoverProject(rootPaths []string) ([]Skill, []ProjectNote) {
	var out []Skill
	var notes []ProjectNote
	seen := map[string]string{} // name → winning source path
	for _, root := range rootPaths {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		for _, relDir := range ProjectSkillDirs() {
			dir := filepath.Join(root, relDir)
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				name := entry.Name()
				skillDir := filepath.Join(dir, name)
				skillFile := filepath.Join(skillDir, "SKILL.md")
				unitID := "skills/" + name

				if _, ok := seen[name]; ok {
					notes = append(notes, ProjectNote{
						Name: name, UnitID: unitID, Note: NoteShadowed, Source: skillFile,
					})
					continue
				}

				fi, err := os.Lstat(skillFile)
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				if fi.Size() > BodyMax {
					notes = append(notes, ProjectNote{
						Name: name, UnitID: unitID, Note: NoteTooLarge, Source: skillFile,
					})
					continue
				}
				if len(out) >= CatalogMax {
					notes = append(notes, ProjectNote{
						Note: NoteCatalogFull, Source: skillFile,
					})
					return out, notes
				}

				// #nosec G304 -- skillFile is a discovered regular file.
				content, err := os.ReadFile(skillFile)
				if err != nil {
					continue
				}
				res := Parse(name, content)
				for _, n := range res.Notes {
					notes = append(notes, ProjectNote{
						Name: name, UnitID: unitID, Note: n, Source: skillFile,
					})
				}
				if res.Skill.Name == "" {
					continue
				}
				sk := res.Skill
				sk.UnitID = unitID
				sk.PackID = ""
				sk.Dir = skillDir
				sk.Project = true
				sk.UserProvided = true
				resourceFS := os.DirFS(skillDir)
				sk.BindResources(resourceFS, ".", DiscoverBundledResources(resourceFS, "."), skillDir)
				out = append(out, sk)
				seen[name] = skillFile
			}
		}
	}
	return out, notes
}

// MergeAdditive fills remaining catalog slots with project skills.
func MergeAdditive(device, project []Skill) ([]Skill, []ProjectNote) {
	have := make(map[string]bool, len(device))
	for _, sk := range device {
		have[sk.UnitID] = true
	}
	out := append([]Skill(nil), device...)
	ordered := append([]Skill(nil), project...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].UnitID < ordered[j].UnitID })
	var notes []ProjectNote
	for _, sk := range ordered {
		src := filepath.Join(sk.Dir, "SKILL.md")
		if have[sk.UnitID] {
			notes = append(notes, ProjectNote{
				Name: sk.Name, UnitID: sk.UnitID, Note: NoteShadowed, Source: src,
			})
			continue
		}
		if len(out) >= CatalogMax {
			notes = append(notes, ProjectNote{
				Name: sk.Name, UnitID: sk.UnitID, Note: NoteCatalogFull, Source: src,
			})
			continue
		}
		out = append(out, sk)
		have[sk.UnitID] = true
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UnitID < out[j].UnitID })
	return out, notes
}
