package skills

import (
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
)

const TemplateResourcesMetadata = "paintedwolf.template_resources"

// Templates are an explicit pack feature; project resources remain literal.
func (s Skill) TemplatesResource(resource string) bool {
	return !s.Project && slices.Contains(s.TemplateResources(), resource)
}

func (s Skill) TemplateResources() []string {
	return strings.FieldsFunc(s.Metadata[TemplateResourcesMetadata], func(r rune) bool { return r == '|' })
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
