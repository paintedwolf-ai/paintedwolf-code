// Package skills parses SKILL.md files.
package skills

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Skill is one loaded SKILL.md document.
type Skill struct {
	Name          string
	Description   string // full, up to the spec bound (Settings + skills_read)
	Body          string
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  string // display metadata only
	// OptionalTools names tools mentioned in the body that are not required dependencies.
	OptionalTools []string
	UnitID   string
	PackID   string
	// Dir is a display-only provenance path.
	Dir              string
	Resources        []string
	ResourcesOmitted int // regular resources beyond the listing bound
	Project          bool
	// UserProvided marks project skills and skills from non-stock packs.
	UserProvided      bool
	resourceFS        fs.FS
	resourceRoot      string
	resourceHostRoot  string
	readableResources map[string]struct{}
}

const (
	NameMaxLen          = 64   // spec
	DescriptionMaxLen   = 1024 // spec
	CompatibilityMaxLen = 500  // spec

	// RosterDescriptionBudget is the rune budget a bundled skill's
	// description spends on every roster line and engine card.
	RosterDescriptionBudget = 180

	BodyMax         = 64 << 10 // host resource limit
	CatalogMax      = 128      // host resource limit
	ResourceListMax = 32       // activation listing bound
	ResourceWalkMax = 512      // entries visited per skill directory walk
	ResourceMax     = 1 << 20  // one on-demand bundled resource
)

// Note codes exported so extpacks maps them without string literals.
const (
	NoteNameInvalid          = "name_invalid"
	NoteNameMismatch         = "name_mismatch"
	NoteFrontmatterInvalid   = "frontmatter_invalid"
	NoteFieldMissing         = "field_missing"
	NoteFieldInvalid         = "field_invalid"
	NoteCompatibilityInvalid = "compatibility_invalid"
	NoteTooLarge             = "too_large"
)

// ParseResult carries a skill or its validation notes.
type ParseResult struct {
	Skill Skill
	Notes []string
}

type frontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license"`
	Compatibility string         `yaml:"compatibility"`
	Metadata      map[string]any `yaml:"metadata"`
	AllowedTools  string         `yaml:"allowed-tools"`
	OptionalTools []string       `yaml:"optional_tools"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName reports whether s is a valid skill name.
func ValidName(s string) bool {
	if len(s) < 1 || len(s) > NameMaxLen {
		return false
	}
	return namePattern.MatchString(s)
}

// Parse reads one SKILL.md for the given directory name.
func Parse(dirName string, content []byte) ParseResult {
	var notes []string
	if !ValidName(dirName) {
		return ParseResult{Notes: []string{NoteNameInvalid}}
	}
	if len(content) > BodyMax {
		return ParseResult{Notes: []string{NoteTooLarge}}
	}

	content = bytes.TrimPrefix(content, []byte("\xEF\xBB\xBF"))
	lines := splitLines(content)
	if len(lines) == 0 || !isFence(lines[0]) {
		return ParseResult{Notes: []string{NoteFrontmatterInvalid}}
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return ParseResult{Notes: []string{NoteFrontmatterInvalid}}
	}

	block := joinLines(lines[1:closeIdx])
	var fm frontmatter
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return ParseResult{Notes: []string{NoteFrontmatterInvalid}}
	}

	desc := strings.TrimSpace(collapseNewlines(fm.Description))
	if desc == "" {
		return ParseResult{Notes: append(notes, NoteFieldMissing)}
	}
	if utf8.RuneCountInString(desc) > DescriptionMaxLen {
		return ParseResult{Notes: append(notes, NoteFieldInvalid)}
	}

	if !frontmatterNameOK(fm.Name, dirName) {
		return ParseResult{Notes: append(notes, NoteNameMismatch)}
	}

	compat := strings.TrimSpace(fm.Compatibility)
	if utf8.RuneCountInString(compat) > CompatibilityMaxLen {
		return ParseResult{Notes: append(notes, NoteCompatibilityInvalid)}
	}

	body := string(joinLines(lines[closeIdx+1:]))
	for strings.HasPrefix(body, "\n") {
		body = strings.TrimPrefix(body, "\n")
	}

	sk := Skill{
		Name:          dirName,
		Description:   desc,
		Body:          body,
		License:       strings.TrimSpace(fm.License),
		Compatibility: compat,
		Metadata:      coerceMetadata(fm.Metadata),
		AllowedTools:  strings.TrimSpace(fm.AllowedTools),
		OptionalTools: append([]string(nil), fm.OptionalTools...),
	}
	return ParseResult{Skill: sk, Notes: notes}
}

// BindResources installs one validated resource catalog.
func (s *Skill) BindResources(fsys fs.FS, root string, catalog ResourceCatalog, hostRoot string) {
	if s == nil {
		return
	}
	s.Resources = append([]string(nil), catalog.Visible...)
	s.ResourcesOmitted = catalog.Omitted
	s.resourceFS = fsys
	s.resourceRoot = root
	s.resourceHostRoot = hostRoot
	s.readableResources = make(map[string]struct{}, len(catalog.Readable))
	for _, name := range catalog.Readable {
		s.readableResources[name] = struct{}{}
	}
}

// SourcePath returns the host-backed location of the skill or a catalog resource.
// An empty resource selects SKILL.md; virtual resources have no host path.
func (s Skill) SourcePath(resource string) string {
	if s.resourceHostRoot == "" {
		return ""
	}
	if resource == "" {
		return filepath.Join(s.resourceHostRoot, "SKILL.md")
	}
	if !fs.ValidPath(resource) || resource == "." {
		return ""
	}
	if _, ok := s.readableResources[resource]; !ok {
		return ""
	}
	return filepath.Join(s.resourceHostRoot, filepath.FromSlash(resource))
}

// ReadResource reads one approved skill resource.
func (s Skill) ReadResource(name string) ([]byte, error) {
	name = strings.TrimSpace(name)
	if !fs.ValidPath(name) || name == "." || path.Base(name) == "SKILL.md" {
		return nil, fmt.Errorf("resource path is not valid")
	}
	if _, ok := s.readableResources[name]; !ok || s.resourceFS == nil {
		return nil, fs.ErrNotExist
	}
	var f fs.File
	var err error
	if s.resourceHostRoot != "" {
		f, err = os.OpenInRoot(s.resourceHostRoot, name)
	} else {
		full := name
		if s.resourceRoot != "." {
			full = path.Join(s.resourceRoot, name)
		}
		f, err = s.resourceFS.Open(full)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > ResourceMax {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("resource is not a readable regular file")
	}
	body, err := io.ReadAll(io.LimitReader(f, ResourceMax+1))
	if err != nil {
		return nil, err
	}
	if len(body) > ResourceMax {
		return nil, fmt.Errorf("resource is over the size limit")
	}
	return body, nil
}

func frontmatterNameOK(name, dirName string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if name != dirName {
		return false
	}
	return ValidName(name)
}

func coerceMetadata(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

func collapseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", " ")
}

func isFence(line string) bool {
	return strings.TrimRight(line, "\r") == "---"
}

func splitLines(b []byte) []string {
	s := string(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func joinLines(lines []string) []byte {
	return []byte(strings.Join(lines, "\n"))
}

