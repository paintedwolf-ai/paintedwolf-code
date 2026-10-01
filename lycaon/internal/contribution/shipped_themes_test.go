package contribution

import (
	"os"
	"path/filepath"
	"testing"
)

// Every shipped theme unit must compile.
func TestShippedThemesCompile(t *testing.T) {
	t.Parallel()

	units := shippedThemeUnits(t)
	if len(units) == 0 {
		t.Fatal("no shipped theme units found; the walk is looking in the wrong place")
	}

	seen := map[string]string{}
	for _, path := range units {
		body, err := os.ReadFile(path) // #nosec G304 -- bundled pack path under test
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		decl, err := DecodeTheme(body)
		if err != nil {
			t.Errorf("%s: decode: %v", filepath.Base(path), err)
			continue
		}
		compiled, err := decl.Compiled()
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if prior, dup := seen[compiled.ID]; dup {
			t.Errorf("%s: id %s already declared by %s", filepath.Base(path), compiled.ID, prior)
		}
		seen[compiled.ID] = filepath.Base(path)
	}
}

// Artwork has no field. Decode rejects unknown keys; compile rejects a
// logomark that is not a visibility.
func TestThemeUnitHasNowhereToPutArtwork(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"logomark svg": `
id: p:t
name: T
appearance: light
brand:
  logomark: "<svg><rect width='64' height='64'/></svg>"
`,
		"sibling artwork key": `
id: p:t
name: T
appearance: light
brand:
  logomark: hidden
  mark: ./wolf.svg
`,
		"top-level artwork key": `
id: p:t
name: T
appearance: light
logo: ./wolf.svg
`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			decl, err := DecodeTheme([]byte(body))
			if err != nil {
				return // rejected at decode
			}
			if _, err := decl.Compiled(); err == nil {
				t.Error("a theme carrying artwork compiled")
			}
		})
	}
}

// shippedThemeUnits walks bundled theme units, not a fixture copy.
func shippedThemeUnits(t *testing.T) []string {
	t.Helper()

	packs := filepath.Join("..", "..", "config", "packs")
	var out []string
	err := filepath.WalkDir(packs, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		// `contributions/themes/` specifically: a pack may itself be named
		// themes, and its manifest is not a theme unit.
		dir := filepath.Dir(path)
		if filepath.Base(dir) != "themes" || filepath.Base(filepath.Dir(dir)) != "contributions" {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", packs, err)
	}
	return out
}
