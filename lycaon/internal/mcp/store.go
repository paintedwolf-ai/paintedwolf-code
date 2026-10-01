package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

const mcpFileMode = 0o600

// UserMCPConfig is the on-disk shape for ~/.config/paintedwolf/mcp.yaml and <overlay>/mcp.yaml.
type UserMCPConfig struct {
	Providers []MCPProviderOverlay `yaml:"providers"`
}

// OverlayUnknownFieldError identifies a key outside the closed mcp.yaml shape.
type OverlayUnknownFieldError struct {
	Field string
}

func (e *OverlayUnknownFieldError) Error() string {
	return fmt.Sprintf("mcp overlay: unknown field %q", e.Field)
}

// IsOverlayUnknownFieldError reports whether an overlay contains an unknown key.
func IsOverlayUnknownFieldError(err error) bool {
	var target *OverlayUnknownFieldError
	return errors.As(err, &target)
}

func projectMCPPath(projectDir string) string {
	return settingsoverlay.ProjectOverlayPath(projectDir, settingsoverlay.BasenameMCP)
}

// LoadUserMCPConfig reads a user or project MCP overlay file.
// Missing file or empty path → empty config. Duplicate ids → DuplicateIDError.
func LoadUserMCPConfig(path string) (*UserMCPConfig, error) {
	if strings.TrimSpace(path) == "" {
		return &UserMCPConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserMCPConfig{}, nil
		}
		return nil, err
	}
	return parseUserMCPConfig(data)
}

// LoadUserMCPConfigLocked reads an overlay under the catalog transaction lock.
// Readers observe only committed contents.
func LoadUserMCPConfigLocked(ctx context.Context, lockRoot, path string) (*UserMCPConfig, error) {
	if strings.TrimSpace(path) == "" {
		return &UserMCPConfig{}, nil
	}
	data, existed, err := catalogruntime.ReadFile(ctx, lockRoot, path)
	if err != nil {
		return nil, err
	}
	if !existed {
		return &UserMCPConfig{}, nil
	}
	return parseUserMCPConfig(data)
}

func parseUserMCPConfig(data []byte) (*UserMCPConfig, error) {
	if err := validateOverlayFields(data); err != nil {
		return nil, err
	}
	var cfg UserMCPConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := duplicateOverlayIDs(cfg.Providers); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func validateOverlayFields(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := dereferenceYAMLNode(doc.Content[0])
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if key != "providers" {
			return &OverlayUnknownFieldError{Field: key}
		}
		providers := dereferenceYAMLNode(root.Content[i+1])
		if providers == nil || providers.Kind != yaml.SequenceNode {
			continue
		}
		for _, provider := range providers.Content {
			if err := validateProviderOverlayFields(provider); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProviderOverlayFields(node *yaml.Node) error {
	node = dereferenceYAMLNode(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	allowed := map[string]struct{}{
		"id": {}, "url": {}, "command": {}, "args": {}, "env": {},
		"headers": {}, "token": {}, "version": {}, "enabled": {},
		"recipe": {}, "credential_wire": {}, "credential_header": {},
		"tool_loading": {},
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := allowed[key]; !ok {
			return &OverlayUnknownFieldError{Field: key}
		}
	}
	return nil
}

func dereferenceYAMLNode(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func saveUserMCPConfig(path string, cfg UserMCPConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     mcpFileMode,
		DirMode:  0o700,
	})
	return err
}

// ProfilesForProvider returns profile names referencing providerID.
func ProfilesForProvider(distro *DistroMCPConfig, providerID string) []string {
	if distro == nil {
		return nil
	}
	var out []string
	for name, ref := range distro.Profiles {
		for _, id := range ref.Providers {
			if id == providerID {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func setProviderEnabled(path, providerID string, enabled bool) error {
	return patchProviderOverlay(path, providerID, func(ov *MCPProviderOverlay) {
		e := enabled
		ov.Enabled = &e
	})
}

func patchProviderOverlay(path, providerID string, mut func(*MCPProviderOverlay)) error {
	cfg, err := LoadUserMCPConfig(path)
	if err != nil {
		return err
	}
	found := false
	for i, s := range cfg.Providers {
		if s.ID == providerID {
			ov := s
			mut(&ov)
			cfg.Providers[i] = ov
			found = true
			break
		}
	}
	if !found {
		ov := MCPProviderOverlay{ID: providerID}
		mut(&ov)
		cfg.Providers = append(cfg.Providers, ov)
	}
	return saveUserMCPConfig(path, *cfg)
}

func upsertProviderOverlay(path string, ov MCPProviderOverlay) error {
	cfg, err := LoadUserMCPConfig(path)
	if err != nil {
		return err
	}
	for i, s := range cfg.Providers {
		if s.ID == ov.ID {
			cfg.Providers[i] = ov
			return saveUserMCPConfig(path, *cfg)
		}
	}
	cfg.Providers = append(cfg.Providers, ov)
	return saveUserMCPConfig(path, *cfg)
}

func clearProviderOverride(path, providerID string) error {
	cfg, err := LoadUserMCPConfig(path)
	if err != nil {
		return err
	}
	next := make([]MCPProviderOverlay, 0, len(cfg.Providers))
	for _, s := range cfg.Providers {
		if s.ID == providerID {
			continue
		}
		next = append(next, s)
	}
	cfg.Providers = next
	if len(cfg.Providers) == 0 {
		if err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.PathLocation(path)}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveUserMCPConfig(path, *cfg)
}

// OverrideIDs returns provider ids present in an overlay file.
func OverrideIDs(cfg *UserMCPConfig) map[string]struct{} {
	out := map[string]struct{}{}
	if cfg == nil {
		return out
	}
	for _, s := range cfg.Providers {
		if s.ID == "" {
			continue
		}
		out[s.ID] = struct{}{}
	}
	return out
}

// ErrUnknownMCPProvider is returned when enable/inherit targets a missing catalog id.
func ErrUnknownMCPProvider(id string) error {
	return AdminErrID(CodeProviderNotFound, id)
}
