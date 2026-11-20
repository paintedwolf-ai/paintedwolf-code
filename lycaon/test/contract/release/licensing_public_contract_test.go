package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browser/designkit"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// redistributableLicenses are the SPDX ids that permit commercial redistribution.
var redistributableLicenses = map[string]bool{"OFL-1.1": true, "ISC": true, "MIT": true, "Apache-2.0": true}

// Every bundled design-kit asset names a redistributable SPDX license whose text
// ships with it, and every listed font is bundled.
func TestDesignKitLicensesAreCommercialFriendly(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "browser", "designkit", "provenance.yaml"))
	contractcheck.FailErr(t, "read designkit provenance", err)
	var doc struct {
		Fonts []struct {
			Family      string `yaml:"family"`
			File        string `yaml:"file"`
			License     string `yaml:"license"`
			LicenseFile string `yaml:"license_file"`
		} `yaml:"fonts"`
		Icons struct {
			License     string `yaml:"license"`
			LicenseFile string `yaml:"license_file"`
		} `yaml:"icons"`
	}
	contractcheck.FailErr(t, "parse designkit provenance", yaml.Unmarshal(raw, &doc))
	if len(doc.Fonts) == 0 {
		t.Fatal("designkit provenance lists no fonts")
	}
	type licensed struct{ name, license, file string }
	entries := []licensed{{"icons", doc.Icons.License, doc.Icons.LicenseFile}}
	for _, font := range doc.Fonts {
		entries = append(entries, licensed{font.Family, font.License, font.LicenseFile})
		name, ok := strings.CutPrefix(font.File, "fonts/")
		if !ok {
			t.Errorf("%s: font file %q is outside fonts/", font.Family, font.File)
			continue
		}
		if data, err := designkit.FontFile(name); err != nil || len(data) == 0 {
			t.Errorf("%s: bundled font %s does not resolve: %v", font.Family, font.File, err)
		}
	}
	for _, entry := range entries {
		if !redistributableLicenses[entry.license] {
			t.Errorf("%s: license %q is not a redistributable SPDX id", entry.name, entry.license)
		}
		if text, err := designkit.LicenseFile(entry.file); err != nil || len(text) == 0 {
			t.Errorf("%s: license file %q does not resolve: %v", entry.name, entry.file, err)
		}
	}
}
