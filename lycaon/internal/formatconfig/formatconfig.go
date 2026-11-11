// Package formatconfig resolves the formatting settings that formatters and
// EditorConfig declare for a project path, in one vocabulary across tools.
package formatconfig

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/editorconfig"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/pkg/api"
)

// Settings are in effect under one tool's configuration. Zero values are
// undeclared, or left to the tool's automatic choice.
type Settings struct {
	LineWidth   int
	IndentStyle editorconfig.IndentStyle
	// IndentSize is columns per level; zero when undeclared or one tab.
	IndentSize int
	// IndentSizeTab is true when a level is one tab and follows TabWidth.
	IndentSizeTab bool
	TabWidth      int
	// EndOfLine is lf, crlf or cr.
	EndOfLine              string
	TrimTrailingWhitespace *bool
	InsertFinalNewline     *bool
	Charset                string
}

// Source is one tool whose configuration is in effect for a path.
type Source struct {
	Tool api.FormatTool
	// Configs are root-relative files that contributed, nearest first.
	Configs  []string
	Settings Settings
}

// MaxFileBytes bounds one configuration file; a larger file declares nothing.
const MaxFileBytes = 1 << 20

// Resolve returns the sources in effect for a root-relative path, in catalog order.
func Resolve(root, rel string) ([]Source, error) {
	var sources []Source
	ec, files, err := editorconfig.LoadWithFiles(root, rel)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		sources = append(sources, Source{Tool: api.FormatToolEditorConfig, Configs: files, Settings: fromEditorConfig(ec)})
	}
	for _, t := range catalog {
		source, found, err := t.resolve(root, rel)
		if err != nil {
			return nil, err
		}
		if found {
			sources = append(sources, source)
		}
	}
	return sources, nil
}

// ConfigNames lists every file name Resolve reads.
func ConfigNames() []string {
	seen := map[string]bool{editorconfig.FileName: true}
	names := []string{editorconfig.FileName}
	for _, t := range catalog {
		for _, f := range t.files {
			if !seen[f.name] {
				seen[f.name] = true
				names = append(names, f.name)
			}
		}
	}
	return names
}

func fromEditorConfig(p editorconfig.Properties) Settings {
	return Settings{
		LineWidth: p.MaxLineLength, IndentStyle: p.IndentStyle, IndentSize: p.IndentSize,
		IndentSizeTab: p.IndentSizeTab, TabWidth: p.TabWidth, EndOfLine: string(p.EndOfLine),
		TrimTrailingWhitespace: p.TrimTrailingWhitespace, InsertFinalNewline: p.InsertFinalNewline,
		Charset: p.Charset,
	}
}

// tool is one formatter and the files it reads.
type tool struct {
	id api.FormatTool
	// files are checked in this order within each directory.
	files []configFile
	// defaults are the tool's documented settings where its configuration is silent.
	defaults Settings
	// settings reads what the configuration declares.
	settings func(doc document) Settings
}

type configFile struct {
	name   string
	format format
	// section is the table that configures the tool; a file without it does
	// not, and the search continues past it.
	section []string
}

// resolve finds the tool's nearest configuration, as the tool itself does.
func (t tool) resolve(root, rel string) (Source, bool, error) {
	for _, dir := range editorconfig.SearchDirs(rel) {
		for _, f := range t.files {
			configPath := path.Join(dir, f.name)
			body, found, err := readFile(root, configPath)
			if err != nil {
				return Source{}, false, err
			}
			if !found {
				continue
			}
			if f.format == formatScript {
				return Source{}, false, nil
			}
			doc, err := decode(f.format, body)
			if err != nil {
				// The tool fails on this file too, so nothing is in effect.
				return Source{}, false, nil
			}
			if len(f.section) > 0 {
				section, ok := doc.table(f.section...)
				if !ok {
					continue
				}
				doc = section
			}
			if doc == nil {
				doc = document{}
			}
			return Source{Tool: t.id, Configs: []string{configPath}, Settings: overlay(t.defaults, t.settings(doc))}, true, nil
		}
	}
	return Source{}, false, nil
}

// overlay fills declared over defaults, field by field.
func overlay(defaults, declared Settings) Settings {
	out := defaults
	if declared.LineWidth > 0 {
		out.LineWidth = declared.LineWidth
	}
	if declared.IndentStyle != "" {
		out.IndentStyle = declared.IndentStyle
	}
	if declared.IndentSize > 0 || declared.IndentSizeTab {
		out.IndentSize, out.IndentSizeTab = declared.IndentSize, declared.IndentSizeTab
	}
	if declared.TabWidth > 0 {
		out.TabWidth = declared.TabWidth
	}
	if declared.EndOfLine != "" {
		out.EndOfLine = declared.EndOfLine
	}
	if declared.TrimTrailingWhitespace != nil {
		out.TrimTrailingWhitespace = declared.TrimTrailingWhitespace
	}
	if declared.InsertFinalNewline != nil {
		out.InsertFinalNewline = declared.InsertFinalNewline
	}
	if declared.Charset != "" {
		out.Charset = declared.Charset
	}
	return out
}

func readFile(root, rel string) ([]byte, bool, error) {
	f, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: filepath.FromSlash(rel)})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", rel, err)
	}
	if len(body) > MaxFileBytes {
		// The file is found but declares nothing readable.
		return []byte{}, true, nil
	}
	return body, true, nil
}
