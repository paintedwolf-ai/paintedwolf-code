package prompts

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
)

// DeclaredReaderFactScalarNames lists the declared scalar reader facts.
func DeclaredReaderFactScalarNames() []string {
	return slices.Clone(declaredReaderFactScalarNames)
}

// ReaderFactFamilies lists the declared reader-fact families.
func ReaderFactFamilies() []ReaderFactFamilySpec {
	return slices.Clone(readerFactFamilies)
}

// CheckOverlayTemplates runs the snapshot's overlay vocabulary check on one directory.
func CheckOverlayTemplates(eff *extpacks.EffectiveCatalog, overlayDir string) ([]string, error) {
	captured := map[string][]byte{}
	if err := capturePromptRoot(overlayDir, captured); err != nil {
		return nil, err
	}
	return checkCapturedOverlayTemplates(eff, captured)
}

// LoadHintCodeRows loads effective policy rows.
func LoadHintCodeRows() (map[string]hintCodeRow, error) {
	entries, err := hintregistry.ListEffective()
	if err != nil {
		return nil, err
	}
	return LoadHintCodeRowsFromEntries(entries)
}

// PreventiveRejectEmitters returns preflight rejection guards.
func PreventiveRejectEmitters() []string {
	out := make([]string, 0, len(preventiveRejectEmitters))
	for emit := range preventiveRejectEmitters {
		out = append(out, emit)
	}
	sort.Strings(out)
	return out
}

// LoadPlaybookMatcher reads playbooks from a host directory — a project overlay
// or a test fixture. A bare directory is not pack content, so no unit gate applies.
func LoadPlaybookMatcher(playbooksDir string, contract *PersonaContract) (*PlaybookMatcher, error) {
	dir := extpacks.OnDisk(playbooksDir)
	if dir.Empty() {
		return nil, fmt.Errorf("playbooks directory required")
	}
	entries, err := dir.List()
	if err != nil {
		return nil, err
	}
	var sources []playbookSource
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		raw, err := dir.Join(ent.Name()).Read()
		if err != nil {
			return nil, err
		}
		sources = append(sources, playbookSource{Name: ent.Name(), Data: raw})
	}
	return loadPlaybookMatcherSources(sources, contract)
}
