//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/packagegeneration"
)

// GTK fixtures use the sealed package catalog's exact helper closure, including
// image loaders on distributions where gdk-pixbuf delegates decoding to Glycin.
func stageGTKFixtureGeneration(dir string, binaries []string) error {
	if err := stageFixtureGeneration(dir, "1.0.1", binaries, []byte("generation-1.0.1\n")); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "linux", "protected-launch.json"))
	if err != nil {
		return err
	}
	var catalog struct {
		Architectures map[string]struct {
			Multiarch string `json:"multiarch"`
		} `json:"architectures"`
		Families map[string]struct {
			Executables []string `json:"system_executables"`
		} `json:"families"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	policyPath := filepath.Join(dir, "launch.json")
	raw, err = os.ReadFile(policyPath)
	if err != nil {
		return err
	}
	var policy GenerationPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return err
	}
	for _, template := range catalog.Families["deb"].Executables {
		path := strings.ReplaceAll(template, "{multiarch}", catalog.Architectures[runtime.GOARCH].Multiarch)
		resolved, err := packagegeneration.ResolveSystemExecutable(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("GTK system helper %s: %w", path, err)
		}
		if !slices.Contains(policy.SystemExecutables, resolved) {
			policy.SystemExecutables = append(policy.SystemExecutables, resolved)
		}
	}
	raw, err = json.Marshal(policy)
	if err != nil {
		return err
	}
	return os.WriteFile(policyPath, raw, 0o644)
}
