package secretmint

import (
	"fmt"
	"slices"
	"strings"
)

// Inspector recognizes catalogued literal credential assignments and measures their material.
type Inspector struct {
	set          map[string]struct{}
	excludedKeys map[string]bool
	catalog      Catalog
	surfaces     map[string][]Surface
	envKeys      map[string]string
	fileKeys     map[string]string
	jsonKeys     map[string]string
	flags        map[string]string
	keyTerms     map[string]struct{}
	keyTermPairs map[[2]string]struct{}
}

// LoadBundled compiles recognition rules and known-password lists.
func LoadBundled() (*Inspector, error) {
	cat, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	vendored, err := loadVendoredPasswords()
	if err != nil {
		return nil, err
	}
	defaults, err := loadDefaultPasswords()
	if err != nil {
		return nil, err
	}
	ins := newInspector(cat, compileSet(vendored, defaults))
	if len(ins.set) == 0 {
		return nil, fmt.Errorf("secret-mint set is empty")
	}
	return ins, nil
}

func newInspector(cat Catalog, set map[string]struct{}) *Inspector {
	ins := &Inspector{
		set:          set,
		excludedKeys: map[string]bool{},
		catalog:      cat,
		surfaces:     map[string][]Surface{},
		envKeys:      make(map[string]string, len(cat.EnvKeys)),
		fileKeys:     make(map[string]string, len(cat.FileKeys)),
		jsonKeys:     make(map[string]string, len(cat.EnvKeys)+len(cat.FileKeys)),
		flags:        make(map[string]string, len(cat.ValueFlags)),
		keyTerms:     make(map[string]struct{}, len(cat.KeyTerms)),
		keyTermPairs: make(map[[2]string]struct{}, len(cat.KeyTermPairs)),
	}
	for tool, surface := range cat.Surfaces {
		ins.addSurface(tool, surface)
	}
	for _, key := range cat.ExcludedKeys {
		ins.excludedKeys[strings.Join(splitKeySegments(key), "_")] = true
	}
	for _, term := range cat.KeyTerms {
		if t := strings.ToLower(strings.TrimSpace(term)); t != "" {
			ins.keyTerms[t] = struct{}{}
		}
	}
	for _, pair := range cat.KeyTermPairs {
		a := strings.ToLower(strings.TrimSpace(pair[0]))
		b := strings.ToLower(strings.TrimSpace(pair[1]))
		if a != "" && b != "" {
			ins.keyTermPairs[[2]string{a, b}] = struct{}{}
		}
	}
	for _, key := range cat.EnvKeys {
		ins.envKeys[strings.ToLower(key)] = key
		ins.jsonKeys[strings.ToLower(key)] = key
	}
	for _, key := range cat.FileKeys {
		folded := strings.ToLower(key)
		if _, exists := ins.fileKeys[folded]; !exists {
			ins.fileKeys[folded] = key
		}
		if _, exists := ins.jsonKeys[folded]; !exists {
			ins.jsonKeys[folded] = key
		}
	}
	for _, flag := range cat.ValueFlags {
		ins.flags[strings.ToLower(flag)] = flag
	}
	return ins
}

func (i *Inspector) addSurface(tool string, surface Surface) {
	if !slices.Contains(i.surfaces[tool], surface) {
		i.surfaces[tool] = append(i.surfaces[tool], surface)
	}
}
