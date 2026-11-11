package skills

import (
	"io/fs"
	"os"
	"sort"
	"strings"
)

// Pack markdown references render through the skill template by default;
// project resources remain literal.
func (s Skill) TemplatesResource(resource string) bool {
	return !s.Project && isPackMarkdownResource(resource)
}

func isPackMarkdownResource(resource string) bool {
	return strings.HasPrefix(resource, "references/") && strings.HasSuffix(resource, ".md")
}

// MarkdownResources lists the bundled pack markdown references that render
// through the skill template, from the skill's readable resource catalog.
func (s Skill) MarkdownResources() []string {
	if s.Project {
		return nil
	}
	names := make([]string, 0, len(s.readableResources))
	for name := range s.readableResources {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		if isPackMarkdownResource(name) {
			out = append(out, name)
		}
	}
	return out
}

type ResourceCatalog struct {
	Visible  []string
	Omitted  int
	Readable []string
}

// DiscoverBundledResources builds a bounded resource catalog.
func DiscoverBundledResources(fsys fs.FS, root string) ResourceCatalog {
	if fsys == nil || strings.TrimSpace(root) == "" {
		return ResourceCatalog{}
	}
	var found []string
	visited := 0
	_ = fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entry
		}
		if path == root {
			return nil
		}
		visited++
		if visited > ResourceWalkMax {
			return fs.SkipAll
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil //nolint:nilerr // skip unreadable metadata
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if d.Name() == "SKILL.md" {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(path, root), "/")
		if rel == "" {
			return nil
		}
		found = append(found, rel)
		return nil
	})
	sort.Strings(found)
	if len(found) <= ResourceListMax {
		return ResourceCatalog{Visible: found, Readable: append([]string(nil), found...)}
	}
	return ResourceCatalog{
		Visible:  append([]string(nil), found[:ResourceListMax]...),
		Omitted:  len(found) - ResourceListMax,
		Readable: found,
	}
}
