// Package protectedpath defines the project paths whose writes are reviewed:
// the files agent-policy loaders read, and credential material.
package protectedpath

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fsname"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// AgentsMDFileName is the instruction file every project root may carry.
const AgentsMDFileName = "AGENTS.md"

// AgentPolicyLocation is one place a project trust surface reads content that
// steers or authorizes agents. Writes there go through the agent-policy
// approval; the host's own state tree is the only write agents never reach.
type AgentPolicyLocation struct {
	// Surface is the project trust surface the loader serves.
	Surface string
	// Name is a basename the loader reads at any depth below a project root.
	Name string
	// Dir is a root-relative directory whose whole tree the loader reads.
	Dir string
	// File is one root-relative file the loader reads.
	File string
}

// Project trust surfaces; the values are the trust registry's surface ids.
const (
	SurfaceAgentsMD             = "agents_md"
	SurfaceSkills               = "skills"
	SurfaceProjectSettings      = "project_settings"
	SurfaceProjectMCP           = "project_mcp"
	SurfaceScanConfig           = "scan_config"
	SurfacePromptOverrides      = "prompt_overrides"
	SurfaceExtensionConfig      = "extension_config"
	SurfaceExtensionSuggestions = "extension_suggestions"
)

// PromptFilesDirName holds a project's prompt replacements inside the overlay.
const PromptFilesDirName = "prompt_files"

// ProjectSkillDirs are the root-relative skill directories, in precedence order.
func ProjectSkillDirs() []string {
	return []string{settingsoverlay.Rel("skills"), filepath.ToSlash(filepath.Join(".agents", "skills"))}
}

// ProjectPromptFilesDir is the root-relative prompt override directory.
func ProjectPromptFilesDir() string { return settingsoverlay.Rel(PromptFilesDirName) }

// AgentPolicyLocations lists every trust-surface location, one declaration
// shared by the loaders, the process floor, and the native write review.
func AgentPolicyLocations() []AgentPolicyLocation {
	out := []AgentPolicyLocation{{Surface: SurfaceAgentsMD, Name: AgentsMDFileName}}
	for _, dir := range ProjectSkillDirs() {
		out = append(out, AgentPolicyLocation{Surface: SurfaceSkills, Dir: dir})
	}
	out = append(out, AgentPolicyLocation{Surface: SurfacePromptOverrides, Dir: ProjectPromptFilesDir()})
	for _, base := range settingsoverlay.SettingsOverlayBasenames() {
		out = append(out, AgentPolicyLocation{Surface: overlaySettingsSurface(base), File: settingsoverlay.Rel(base)})
	}
	out = append(out,
		AgentPolicyLocation{Surface: SurfaceProjectSettings, File: settingsoverlay.Rel(settingsoverlay.BasenamePostures)},
		AgentPolicyLocation{Surface: SurfaceProjectSettings, File: settingsoverlay.Rel(settingsoverlay.FormatFileName)},
		AgentPolicyLocation{Surface: SurfaceProjectSettings, Dir: settingsoverlay.Rel(settingsoverlay.RulesDirName)},
		AgentPolicyLocation{Surface: SurfaceProjectSettings, Dir: settingsoverlay.Rel(settingsoverlay.WorkflowsDirName)},
	)
	for _, base := range settingsoverlay.ScanConfigBasenames() {
		out = append(out, AgentPolicyLocation{Surface: SurfaceScanConfig, File: settingsoverlay.Rel(base)})
	}
	return out
}

func overlaySettingsSurface(base string) string {
	switch base {
	case settingsoverlay.BasenameMCP:
		return SurfaceProjectMCP
	case settingsoverlay.BasenameExtensions, settingsoverlay.BasenameExtensionsLock:
		return SurfaceExtensionConfig
	default:
		return SurfaceProjectSettings
	}
}

// Contains reports whether rel, a path relative to a project root, lies in
// this location. Each component folds case and filesystem respellings.
func (l AgentPolicyLocation) Contains(rel string) bool {
	parts := canonicalParts(rel)
	if len(parts) == 0 || parts[0] == ".." {
		return false
	}
	switch {
	case l.Name != "":
		return strings.EqualFold(parts[len(parts)-1], l.Name)
	case l.File != "":
		return partsEqual(parts, canonicalParts(l.File))
	default:
		dir := canonicalParts(l.Dir)
		return len(parts) >= len(dir) && partsEqual(parts[:len(dir)], dir)
	}
}

// NameRegex matches an absolute path whose last component is a Name location,
// folding case explicitly. Kernel rules pair it with a subpath per root, so
// the expression stays one basename long however many roots are registered.
// File and Dir locations are kernel literals and subpaths, and return "".
func (l AgentPolicyLocation) NameRegex() string {
	if l.Name == "" {
		return ""
	}
	return "/" + foldedLiteral(l.Name) + "$"
}

// Path joins a File or Dir location onto root.
func (l AgentPolicyLocation) Path(root string) string {
	if l.File != "" {
		return filepath.Join(root, filepath.FromSlash(l.File))
	}
	return filepath.Join(root, filepath.FromSlash(l.Dir))
}

func canonicalParts(rel string) []string {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "" || rel == "." {
		return nil
	}
	var parts []string
	for _, part := range strings.Split(strings.TrimPrefix(rel, "/"), "/") {
		// Respelling folds strip trailing dots, so parent steps are kept first.
		if part == ".." {
			return []string{".."}
		}
		if part = fsname.CanonicalBasename(part); part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func partsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func foldedLiteral(name string) string {
	var b strings.Builder
	for _, r := range name {
		lower, upper := strings.ToLower(string(r)), strings.ToUpper(string(r))
		// A fold that changes length (ß to SS) cannot be one character class.
		if lower == upper || utf8.RuneCountInString(lower) != 1 || utf8.RuneCountInString(upper) != 1 {
			b.WriteString(regexp.QuoteMeta(string(r)))
			continue
		}
		b.WriteString("[" + upper + lower + "]")
	}
	return b.String()
}
