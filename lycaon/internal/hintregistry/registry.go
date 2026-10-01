// Package hintregistry loads the per-code policy registry (flat policy/).
package hintregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"gopkg.in/yaml.v3"
)

// DefaultDir is the platform policy directory, used when a caller loads one explicit
// directory rather than the ListEffective union of pack policy/ dirs.
const DefaultDir = config.PlatformPolicy

// Entry is one policy document, bundled or on a host overlay.
type Entry struct {
	Code string
	Path extpacks.Source
	Body []byte
}

// ListEffective loads policy units from every contributing pack — stock and
// installed — through the process catalog (resolving committed device state
// when none is active yet).
func ListEffective() ([]Entry, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return ListEffectiveWithCatalog(catalog)
}

// ListEffectiveWithCatalog compiles policy units from the resolved winners'
// captured bytes. Parsed entries are cached by policy contribution digest.
func ListEffectiveWithCatalog(catalog *extpacks.EffectiveCatalog) ([]Entry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("policy registry: effective catalog required")
	}
	key := EffectivePolicyKey(catalog)
	if entries, ok := effectiveEntries.lookup(key); ok {
		return entries, nil
	}
	entries, err := parseEffectiveEntries(catalog)
	if err != nil {
		return nil, err
	}
	effectiveEntries.store(key, entries)
	return entries, nil
}

// effectiveEntriesCap bounds retained registries: one stock set plus a few
// project overlays.
const effectiveEntriesCap = 4

var effectiveEntries = policyRegistryCache{entries: map[string][]Entry{}}

type policyRegistryCache struct {
	mu      sync.Mutex
	entries map[string][]Entry
	order   []string
}

func (c *policyRegistryCache) lookup(key string) ([]Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return slices.Clone(entries), true
}

func (c *policyRegistryCache) store(key string, entries []Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		return
	}
	if len(c.order) == effectiveEntriesCap {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
	c.entries[key] = slices.Clone(entries)
	c.order = append(c.order, key)
}

// EffectivePolicyKey digests everything the registry reads: each policy
// contribution, because validation examines losers as well as winners, and
// each winning body.
func EffectivePolicyKey(catalog *extpacks.EffectiveCatalog) string {
	if catalog == nil {
		return ""
	}
	ids := make([]string, 0, len(catalog.Units))
	for id := range catalog.Units {
		if extpacks.KindRootForUnitID(id) == "policy" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	h := sha256.New()
	field := func(label string, body []byte) {
		_, _ = fmt.Fprintf(h, "%s\x1f%d\x1f", label, len(body))
		_, _ = h.Write(body)
	}
	for _, id := range ids {
		unit := catalog.Units[id]
		field("unit", []byte(id))
		for _, contribution := range unit.Contributions {
			field("pack", []byte(contribution.PackID))
			field("path", []byte(contribution.Path.String()))
			field("content", contribution.Content)
		}
		if content, packID, ok := catalog.UnitContent(id); ok {
			field("winner", []byte(packID))
			field("body", content)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseEffectiveEntries(catalog *extpacks.EffectiveCatalog) ([]Entry, error) {
	if err := catalog.ValidateOARPolicies(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Entry
	for _, id := range catalog.LoadedUnitIDs() {
		if !strings.HasPrefix(id, "policy/") {
			continue
		}
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		at, _ := catalog.UnitPath(id)
		code, flat, err := parsePolicyFile(at, content)
		if err != nil {
			return nil, err
		}
		// [OAR-DOC-9] Effective entries retain unique document identities.
		if seen[code] {
			return nil, fmt.Errorf("%s: duplicate policy code %q", at, code)
		}
		seen[code] = true
		out = append(out, Entry{Code: code, Path: at, Body: flat})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("policy registry: no policy entries found after resolve filter")
	}
	return out, nil
}

// codeFromPolicyPath reads the presentation key for a hint_codes document.
// OAR documents derive their identity from namespace/id instead.
func codeFromPolicyPath(at extpacks.Source) string {
	name := policyFileName(at)
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// policyFileName is the final segment of at, for either kind of source.
func policyFileName(at extpacks.Source) string {
	return filepath.Base(at.String())
}

// List reads and validates JSON or YAML policy documents under dir.
func List(dir extpacks.Source) ([]Entry, error) {
	info, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: policy registry path must be a directory", dir)
	}
	names, err := dir.List()
	if err != nil {
		return nil, err
	}
	// A bare directory has no pack identity — this path is for project overlays
	// and tests, where there is nothing to win or lose an `own` against.
	var files []extpacks.PackFile
	for _, ent := range names {
		if ent.IsDir() || extpacks.UnitIDFor("", "policy/"+ent.Name()) == "" {
			continue
		}
		files = append(files, extpacks.PackFile{Path: dir.Join(ent.Name())})
	}
	return entriesFromFiles(files)
}

func entriesFromFiles(files []extpacks.PackFile) ([]Entry, error) {
	seen := map[string]bool{}
	var out []Entry
	for _, f := range files {
		data, err := f.Path.Read()
		if err != nil {
			return nil, err
		}
		code, flat, err := parsePolicyFile(f.Path, data)
		if err != nil {
			return nil, err
		}
		if seen[code] {
			return nil, fmt.Errorf("%s: duplicate policy code %q", f.Path, code)
		}
		seen[code] = true
		out = append(out, Entry{Code: code, Path: f.Path, Body: flat})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("policy registry: no policy entries found")
	}
	return out, nil
}

// parsePolicyFile accepts a flat policy document, or a project overlay's
// single-entry hint_codes wrapper naming the same code. It returns the code
// plus the flat body every consumer decodes.
func parsePolicyFile(at extpacks.Source, data []byte) (string, []byte, error) {
	data, err := extpacks.NormalizePolicyDocument(data)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", at, err)
	}
	var probe map[string]any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return "", nil, fmt.Errorf("%s: %w", at, err)
	}
	code := codeFromPolicyPath(at)
	if code == "" || code == policyFileName(at) {
		return "", nil, fmt.Errorf("%s: expected a named policy document", at)
	}
	if hc, has := probe["hint_codes"]; has {
		m, ok := hc.(map[string]any)
		if !ok || len(m) != 1 {
			return "", nil, fmt.Errorf("%s: expected exactly one hint_codes entry", at)
		}
		for c := range m {
			if c != code {
				return "", nil, fmt.Errorf("%s: filename must match hint code %q", at, c)
			}
			body, err := yaml.Marshal(m[c])
			if err != nil {
				return "", nil, fmt.Errorf("%s: flatten: %w", at, err)
			}
			return c, body, nil
		}
	}
	identity, _, oar, err := extpacks.PolicyDocumentIdentity(data)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", at, err)
	}
	if oar {
		return identity, data, nil
	}
	return code, data, nil
}
