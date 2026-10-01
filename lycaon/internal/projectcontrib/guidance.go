package projectcontrib

import (
	"bytes"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

const skillFileName = "SKILL.md"

func scanGuidanceIndex(roots []string, indices map[string][]governance.ResolvedAgentsMD) Surface {
	out := Surface{ID: SurfaceAgentsMD}
	for _, root := range roots {
		for _, entry := range indices[root] {
			rel := entry.Path
			full := filepath.Join(root, rel)
			data, err := readSurfaceFile(&out, root, full)
			if err != nil {
				continue
			}
			retainFile(&out, root, full, data)
			item := Item{Name: rel, RootPath: root, Path: rel, Lines: countLines(data)}
			out.Items = append(out.Items, item)
		}
		rel := settingsoverlay.Rel(protectedpath.AgentsMDFileName)
		full := filepath.Join(root, rel)
		data, readErr := readSurfaceFile(&out, root, full)
		if readErr == nil {
			retainFile(&out, root, full, data)
			rel = filepath.ToSlash(rel)
			out.Items = append(out.Items, Item{
				Name: rel, RootPath: root, Path: rel, Lines: countLines(data),
			})
		}
	}
	return finishSurface(out)
}

func scanSkills(roots []string) Surface {
	out := Surface{ID: SurfaceSkills}
	seen := make(map[string]struct{})
	for _, root := range roots {
		for _, relDir := range protectedpath.ProjectSkillDirs() {
			dir := filepath.Join(root, filepath.FromSlash(relDir))
			entries := readSurfaceDir(&out, root, dir)
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				manifest := filepath.Join(dir, name, skillFileName)
				// The first readable definition of a skill name wins.
				if _, dup := seen[name]; dup {
					continue
				}
				data, readErr := readSurfaceFile(&out, root, manifest)
				if readErr != nil {
					continue
				}
				retainFile(&out, root, manifest, data)
				seen[name] = struct{}{}
				rel := relFrom(root, manifest)
				out.Items = append(out.Items, Item{Name: name, RootPath: root, Path: rel})
			}
		}
	}
	return finishSurface(out)
}

func relFrom(root, full string) string {
	if rel, err := filepath.Rel(root, full); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(full)
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if !bytes.HasSuffix(data, []byte{'\n'}) {
		n++
	}
	return n
}
