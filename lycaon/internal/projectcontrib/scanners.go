package projectcontrib

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// mcpOverlayFile contains fields that determine MCP reach.
type mcpOverlayFile struct {
	Providers []struct {
		ID      string    `yaml:"id"`
		URL     *string   `yaml:"url,omitempty"`
		Command *string   `yaml:"command,omitempty"`
		Args    *[]string `yaml:"args,omitempty"`
		Enabled *bool     `yaml:"enabled,omitempty"`
	} `yaml:"providers"`
}

// extensionsOverlayFile feeds both extension Trust surfaces.
type extensionsOverlayFile struct {
	Suggest []struct {
		ID      string `yaml:"id"`
		Source  string `yaml:"source,omitempty"`
		Version string `yaml:"version,omitempty"`
		Ref     string `yaml:"ref,omitempty"`
	} `yaml:"suggest"`
	Disabled []string `yaml:"disabled"`
}

func scanProjectMCP(roots []string) Surface {
	out := Surface{ID: SurfaceProjectMCP}
	var reviewItems []mcpReviewItem
	for _, root := range roots {
		path := settingsoverlay.ProjectOverlayPath(root, settingsoverlay.BasenameMCP)
		data, err := readSurfaceFile(&out, root, path)
		if err != nil {
			continue
		}
		retainFile(&out, root, path, data)
		var file mcpOverlayFile
		if err := yaml.Unmarshal(data, &file); err != nil {
			continue
		}
		for _, srv := range file.Providers {
			id := strings.TrimSpace(srv.ID)
			if id == "" {
				continue
			}
			if srv.Enabled != nil && !*srv.Enabled {
				continue
			}
			detail, transport := mcpDetail(srv.URL, srv.Command, srv.Args)
			reviewItems = append(reviewItems, mcpReviewItem{
				item: Item{
					Name: id, Detail: detail, RootPath: root, Path: relFrom(root, path),
				},
				transport: transport,
			})
		}
	}
	// Show local subprocesses before remote connections.
	sort.SliceStable(reviewItems, func(i, j int) bool {
		li, lj := reviewItems[i].transport == mcpTransportLocal, reviewItems[j].transport == mcpTransportLocal
		if li != lj {
			return li
		}
		return reviewItems[i].item.Name < reviewItems[j].item.Name
	})
	for _, reviewItem := range reviewItems {
		out.Items = append(out.Items, reviewItem.item)
	}
	out.Count = len(out.Items)
	return out
}

type mcpTransport string

const (
	mcpTransportLocal  mcpTransport = "local"
	mcpTransportRemote mcpTransport = "remote"
	mcpTransportNone   mcpTransport = "none"
)

type mcpReviewItem struct {
	item      Item
	transport mcpTransport
}

func mcpDetail(url, command *string, args *[]string) (detail string, transport mcpTransport) {
	if command != nil && strings.TrimSpace(*command) != "" {
		line := strings.TrimSpace(*command)
		if args != nil {
			for _, a := range *args {
				line += " " + a
			}
		}
		return "runs locally · " + line, mcpTransportLocal
	}
	if url != nil && strings.TrimSpace(*url) != "" {
		return "remote · " + strings.TrimSpace(*url), mcpTransportRemote
	}
	return "no transport declared", mcpTransportNone
}

func scanExtensionConfig(roots []string) Surface {
	out := Surface{ID: SurfaceExtensionConfig}
	for _, root := range roots {
		desiredPath := settingsoverlay.ProjectOverlayPath(root, settingsoverlay.BasenameExtensions)
		data, err := readSurfaceFile(&out, root, desiredPath)
		if err != nil {
			continue
		}
		retainFile(&out, root, desiredPath, data)
		var file extensionsOverlayFile
		if err := yaml.Unmarshal(data, &file); err != nil {
			continue
		}
		for _, unitID := range file.Disabled {
			unitID = strings.TrimSpace(unitID)
			if unitID == "" {
				continue
			}
			out.Items = append(out.Items, Item{
				Name: unitID, Detail: "disabled", RootPath: root, Path: relFrom(root, desiredPath),
			})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	out.Count = len(out.Items)
	return out
}

func scanExtensionSuggestions(roots []string) Surface {
	out := Surface{ID: SurfaceExtensionSuggestions}
	for _, root := range roots {
		desiredPath := settingsoverlay.ProjectOverlayPath(root, settingsoverlay.BasenameExtensions)
		data, err := readSurfaceFile(&out, root, desiredPath)
		if err != nil {
			continue
		}
		retainFile(&out, root, desiredPath, data)
		for _, item := range extensionSuggestionItems(data) {
			item.RootPath = root
			item.Path = relFrom(root, desiredPath)
			out.Items = append(out.Items, item)
		}
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	out.Count = len(out.Items)
	return out
}

func extensionSuggestionItems(data []byte) []Item {
	var file extensionsOverlayFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil
	}
	var items []Item
	for _, pack := range file.Suggest {
		id := strings.TrimSpace(pack.ID)
		if id == "" {
			continue
		}
		detail := extensionPackDetail(pack.Source, pack.Version, pack.Ref)
		items = append(items, Item{Name: id, Detail: detail})
	}
	return items
}

func extensionPackDetail(source, version, ref string) string {
	detail := strings.TrimSpace(source)
	switch {
	case strings.TrimSpace(ref) != "":
		detail = strings.TrimSpace(detail + " @ " + strings.TrimSpace(ref))
	case strings.TrimSpace(version) != "":
		detail = strings.TrimSpace(detail + " @ " + strings.TrimSpace(version))
	}
	return detail
}

// projectSettingsBasenames excludes files managed by other trust surfaces.
func projectSettingsBasenames() []string {
	skip := map[string]bool{
		settingsoverlay.BasenameMCP:            true,
		settingsoverlay.BasenameExtensions:     true,
		settingsoverlay.BasenameExtensionsLock: true,
	}
	for _, name := range settingsoverlay.ScanConfigBasenames() {
		skip[name] = true
	}
	var files []string
	for _, base := range settingsoverlay.SettingsOverlayBasenames() {
		if !skip[base] {
			files = append(files, base)
		}
	}
	return append(files, settingsoverlay.BasenamePostures, settingsoverlay.FormatFileName)
}

func scanProjectSettings(roots []string) Surface {
	out := Surface{ID: SurfaceProjectSettings}
	for _, root := range roots {
		base := filepath.Join(root, settingsoverlay.DirName())
		for _, name := range projectSettingsBasenames() {
			collectFile(&out, root, filepath.Join(base, name), name)
		}
		collectDir(&out, root, filepath.Join(base, settingsoverlay.RulesDirName), settingsoverlay.RulesDirName, false)
		collectWorkflows(&out, root, base)
	}
	return finishSurface(out)
}

func scanScanConfig(roots []string) Surface {
	out := Surface{ID: SurfaceScanConfig}
	for _, root := range roots {
		base := filepath.Join(root, settingsoverlay.DirName())
		for _, name := range settingsoverlay.ScanConfigBasenames() {
			collectFile(&out, root, filepath.Join(base, name), name)
		}
	}
	return finishSurface(out)
}

func scanPromptOverrides(roots []string) Surface {
	out := Surface{ID: SurfacePromptOverrides}
	for _, root := range roots {
		dir := filepath.Join(root, filepath.FromSlash(protectedpath.ProjectPromptFilesDir()))
		collectDir(&out, root, dir, protectedpath.PromptFilesDirName, true)
	}
	return finishSurface(out)
}

func collectFile(out *Surface, root, full, name string) {
	data, err := readSurfaceFile(out, root, full)
	if err != nil {
		return
	}
	retainFile(out, root, full, data)
	out.Items = append(out.Items, Item{Name: name, RootPath: root, Path: relFrom(root, full)})
}

func collectDir(out *Surface, root, dir, prefix string, recurse bool) {
	entries := readSurfaceDir(out, root, dir)
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		rel := prefix + "/" + e.Name()
		if e.IsDir() {
			if recurse {
				collectDir(out, root, child, rel, true)
			}
			continue
		}
		collectFile(out, root, child, rel)
	}
}

func collectWorkflows(out *Surface, root, base string) {
	dirs := readSurfaceDir(out, root, filepath.Join(base, settingsoverlay.WorkflowsDirName))
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(settingsoverlay.WorkflowsDirName, dir.Name(), "workflow.yaml"))
		collectFile(out, root, filepath.Join(base, settingsoverlay.WorkflowsDirName, dir.Name(), "workflow.yaml"), rel)
	}
}

func finishSurface(out Surface) Surface {
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	out.Count = len(out.Items)
	return out
}

const maxTrustFileBytes = 4 << 20
const maxTrustSnapshotBytes = 32 << 20
const maxTrustSnapshotFiles = 4096

func readSurfaceDir(out *Surface, root, dir string) []os.DirEntry {
	if out.ReadError != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		out.ReadError = fmt.Errorf("read project configuration directory %s: %w", relFrom(root, dir), err)
	}
	return entries
}

func readSurfaceFile(out *Surface, root, full string) ([]byte, error) {
	if out.ReadError != nil {
		return nil, out.ReadError
	}
	data, err := readContribution(root, relFrom(root, full))
	if err == nil && len(out.Files) >= maxTrustSnapshotFiles {
		err = errors.New("project configuration exceeds the 4096 file review limit")
	}
	if err == nil && out.CapturedBytes+len(data) > maxTrustSnapshotBytes {
		err = errors.New("project configuration exceeds the 32 MB review limit")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		out.ReadError = fmt.Errorf("read project configuration %s: %w", relFrom(root, full), err)
	}
	return data, err
}

func readContribution(root, path string) ([]byte, error) {
	file, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: path})
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("configuration must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTrustFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTrustFileBytes {
		return nil, errors.New("configuration exceeds the 4 MB review limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("configuration is not valid UTF-8 text")
	}
	return data, nil
}

func retainFile(out *Surface, root, full string, data []byte) {
	out.CapturedBytes += len(data)
	sum := sha256.Sum256(data)
	out.Files = append(out.Files, File{RootPath: root, Path: relFrom(root, full), Content: string(data), SHA256: hex.EncodeToString(sum[:])})
}
