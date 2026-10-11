package blueprintfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/fseffect"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// ParseBlueprintFrontmatter reads YAML frontmatter from a markdown file and returns selected keys.
// Missing or corrupt frontmatter yields an empty map (not an error).
func ParseBlueprintFrontmatter(projectDir, relPath string, keys []string) (map[string]string, error) {
	content, err := ReadBlueprintFile(projectDir, relPath)
	if err != nil {
		return nil, err
	}
	fm, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	if len(fm) == 0 {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if v, ok := fm[key]; ok && v != nil {
			out[key] = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return out, nil
}

// LoadBlueprintView loads manifest blueprint frontmatter for inject.
func LoadBlueprintView(projectDir string, bp *workflowdef.BlueprintDef) (map[string]string, error) {
	if bp == nil {
		return nil, nil
	}
	return ParseBlueprintFrontmatter(projectDir, bp.Path, bp.Frontmatter)
}

// WriteBlueprintFile writes governing markdown to a project-relative convention
// path, creating the convention directory and refusing symlink traversal at any
// component beneath projectDir.
func WriteBlueprintFile(projectDir, relPath, content string) error {
	projectDir = strings.TrimSpace(projectDir)
	relPath = strings.TrimSpace(relPath)
	if projectDir == "" || relPath == "" || !filepath.IsAbs(projectDir) {
		return os.ErrInvalid
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: projectDir, Rel: filepath.FromSlash(relPath)},
		Source:   strings.NewReader(content),
		Mode:     blueprint.FileMode,
		DirMode:  blueprint.DirMode,
	})
	return err
}

// ParsePlanBlueprintFrontmatter reads YAML frontmatter keys from plan markdown content.
func ParsePlanBlueprintFrontmatter(content string, keys []string) map[string]string {
	fm, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	if len(fm) == 0 {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if v, ok := fm[key]; ok && v != nil {
			out[key] = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return out
}

// ReadBlueprintFile reads a project-relative blueprint path.
func ReadBlueprintFile(projectDir, relPath string) (string, error) {
	projectDir = strings.TrimSpace(projectDir)
	relPath = strings.TrimSpace(relPath)
	if projectDir == "" || relPath == "" {
		return "", os.ErrInvalid
	}
	data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(relPath)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
