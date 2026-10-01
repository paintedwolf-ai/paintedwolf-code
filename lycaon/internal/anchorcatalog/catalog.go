// Package anchorcatalog loads the anchor catalog.
package anchorcatalog

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// Dedup is the optional dedup: block on a catalog Anchor row.
type Dedup struct {
	Key            string   `yaml:"key"`
	OmitHintCodes  []string `yaml:"omit_hint_codes"`
	OmitInformWhen []string `yaml:"omit_inform_when"`
}

// Catalog is the parsed anchors/catalog.yaml.
type Catalog struct {
	IDs      map[string]bool
	Dedup    map[string]Dedup
	Surfaces map[string]string
}

type fileDoc struct {
	Anchors []struct {
		ID      string `yaml:"id"`
		Surface string `yaml:"surface"`
		Dedup   *Dedup `yaml:"dedup"`
	} `yaml:"anchors"`
}

var (
	mu        sync.RWMutex
	installed *Catalog
)

// Load reads catalog.yaml and returns ids + dedup policies (does not Install).
func Load(path string) (*Catalog, error) {
	// #nosec G304 -- path names the selected anchor catalog.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read anchor catalog: %w", err)
	}
	return parse(raw)
}

func parse(raw []byte) (*Catalog, error) {
	var doc fileDoc
	// The runtime indexes only dispatch fields from the richer catalog document.
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse anchor catalog: %w", err)
	}
	out := &Catalog{
		IDs:      make(map[string]bool, len(doc.Anchors)),
		Dedup:    make(map[string]Dedup),
		Surfaces: make(map[string]string, len(doc.Anchors)),
	}
	for _, a := range doc.Anchors {
		id := strings.TrimSpace(a.ID)
		if id == "" {
			continue
		}
		out.IDs[id] = true
		out.Surfaces[id] = strings.TrimSpace(a.Surface)
		if a.Dedup != nil {
			out.Dedup[id] = Dedup{
				Key:            strings.TrimSpace(a.Dedup.Key),
				OmitHintCodes:  append([]string(nil), a.Dedup.OmitHintCodes...),
				OmitInformWhen: append([]string(nil), a.Dedup.OmitInformWhen...),
			}
		}
	}
	if len(out.IDs) == 0 {
		return nil, fmt.Errorf("anchor catalog: no anchors")
	}
	return out, nil
}

// Install sets the process-wide catalog used by Bindings and OAR on: checks.
func Install(cat *Catalog) {
	if cat == nil || len(cat.IDs) == 0 {
		panic("anchorcatalog: Install requires a non-empty catalog")
	}
	ids := make(map[string]bool, len(cat.IDs))
	for k, v := range cat.IDs {
		if v {
			ids[k] = true
		}
	}
	dedup := make(map[string]Dedup, len(cat.Dedup))
	for id, d := range cat.Dedup {
		dedup[id] = Dedup{
			Key:            d.Key,
			OmitHintCodes:  append([]string(nil), d.OmitHintCodes...),
			OmitInformWhen: append([]string(nil), d.OmitInformWhen...),
		}
	}
	surfaces := make(map[string]string, len(cat.Surfaces))
	for id, surface := range cat.Surfaces {
		surfaces[id] = surface
	}
	mu.Lock()
	installed = &Catalog{IDs: ids, Dedup: dedup, Surfaces: surfaces}
	mu.Unlock()
}

// InstallFile loads path and Installs.
func InstallFile(path string) error {
	cat, err := Load(path)
	if err != nil {
		return err
	}
	Install(cat)
	return nil
}

// InstallBundled installs the anchor catalog that ships in the binary.
func InstallBundled() error {
	raw, err := config.Read(config.AnchorCatalog)
	if err != nil {
		return fmt.Errorf("read anchor catalog: %w", err)
	}
	cat, err := parse(raw)
	if err != nil {
		return err
	}
	Install(cat)
	return nil
}

// Clear clears process catalog state (tests only).
func Clear() {
	mu.Lock()
	installed = nil
	mu.Unlock()
}

// Loaded reports whether Install has run.
func Loaded() bool {
	mu.RLock()
	defer mu.RUnlock()
	return installed != nil && len(installed.IDs) > 0
}

// Has reports whether id is an exact catalog.yaml row.
func Has(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	mu.RLock()
	defer mu.RUnlock()
	return installed != nil && installed.IDs[id]
}

// Require errors when the catalog is missing or id is unknown.
func Require(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("anchorcatalog: empty id")
	}
	mu.RLock()
	loaded := installed != nil && len(installed.IDs) > 0
	ok := loaded && installed.IDs[id]
	mu.RUnlock()
	if !loaded {
		return fmt.Errorf("anchorcatalog: catalog not installed (load anchors/catalog.yaml before Bindings/Emit/OAR)")
	}
	if !ok {
		return fmt.Errorf("anchorcatalog: unknown id %q (not in anchors/catalog.yaml)", id)
	}
	return nil
}

// DedupFor returns the catalog dedup policy for id, if any.
func DedupFor(id string) (Dedup, bool) {
	mu.RLock()
	defer mu.RUnlock()
	if installed == nil {
		return Dedup{}, false
	}
	d, ok := installed.Dedup[strings.TrimSpace(id)]
	return d, ok
}

// SurfaceFor returns the catalog-defined surface for an anchor.
func SurfaceFor(id string) string {
	mu.RLock()
	defer mu.RUnlock()
	if installed == nil {
		return ""
	}
	return installed.Surfaces[strings.TrimSpace(id)]
}
