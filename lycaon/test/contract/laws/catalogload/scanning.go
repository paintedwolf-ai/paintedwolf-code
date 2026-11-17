package catalogload

import (
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/conditions"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/toolschema"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/laws"
)

const scanSchemaDir = "lycaon/config/packs/painted-wolf/platform/tools/schemas"

// catalogOwnedScanTools take their description and argument schema from
// the catalog.
var catalogOwnedScanTools = []string{"scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare"}

func scanSchemaEntry(r *laws.Run, tool string) toolschema.Entry {
	var entry toolschema.Entry
	r.Must(yaml.Unmarshal(r.Bytes(scanSchemaDir+"/"+tool+".yaml"), &entry))
	return entry
}

// schemaEnum walks a decoded schema to a string enum.
func schemaEnum(r *laws.Run, schema map[string]any, keys ...string) []string {
	cur := any(schema)
	for _, key := range keys {
		node, ok := cur.(map[string]any)
		if !ok {
			r.Fatalf("schema path %s does not lead to a mapping", strings.Join(keys, "."))
		}
		cur = node[key]
	}
	raw, ok := cur.([]any)
	if !ok {
		r.Fatalf("schema path %s is not a list", strings.Join(keys, "."))
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			r.Fatalf("schema path %s holds the non-string %v", strings.Join(keys, "."), v)
		}
		out = append(out, s)
	}
	return out
}

var scanning = []laws.Rule{{
	// The catalog defines each scan tool's description and schema.
	Name:    "scan-tools-are-catalog-owned",
	Anchors: goFileAnchors(scanSchemaDir, catalogOwnedScanTools),
	Check: func(r *laws.Run) {
		for _, name := range catalogOwnedScanTools {
			entry := scanSchemaEntry(r, name)
			if strings.TrimSpace(entry.Description) == "" || entry.Schema == nil {
				r.Report("%s.yaml needs a description and a schema", name)
			}
		}
	},
}, {
	// Scan tool enums equal the wire vocabularies.
	Name:    "scan-tool-catalog-enums-match-the-wire",
	Anchors: []laws.Anchor{laws.Func("pkg/api", "AllScanCategoryValues"), laws.Func("pkg/api", "AllFindingLevelValues")},
	Check: func(r *laws.Run) {
		categories := []string{"all"}
		for _, c := range api.AllScanCategoryValues() {
			categories = append(categories, string(c))
		}
		r.Diff("scan_pack categories", categories, schemaEnum(r, scanSchemaEntry(r, "scan_pack").Schema, "properties", "categories", "items", "enum"))
		var levels []string
		for _, l := range api.AllFindingLevelValues() {
			levels = append(levels, string(l))
		}
		r.Diff("scan_query level", levels, schemaEnum(r, scanSchemaEntry(r, "scan_query").Schema, "properties", "level", "enum"))
	},
}, {
	// Bundled workflows complete on implemented conditions, never a
	// deferred scan stub.
	Name:    "bundled-workflows-avoid-deferred-scan-stubs",
	Anchors: []laws.Anchor{laws.Func("internal/conditions", "ScanCatalogStubIDs")},
	Check: func(r *laws.Run) {
		stubs := conditions.ScanCatalogStubIDs()
		r.Expect(len(stubs), "deferred scan stubs")
		reg, err := workflowdef.RegistryFromDirs("")
		r.Must(err)
		phases := 0
		for key, m := range reg.All() {
			for _, def := range m.PhaseDefs {
				phases++
				for _, stub := range stubs {
					if strings.Contains(def.CompleteWhen, stub) {
						r.Report("%s phase %s completes on the deferred scan stub %s", key, def.ID, stub)
					}
				}
			}
		}
		r.Expect(phases, "bundled workflow phases")
	},
}, {
	// Every scanner names its categories and driver, and an internal driver
	// its implementation.
	Name:    "scanner-config-loads",
	Anchors: []laws.Anchor{laws.Func("internal/scan/catalog", "LoadScannerConfig")},
	Check: func(r *laws.Run) {
		cfg, err := scancatalog.LoadScannerConfig()
		r.Must(err)
		r.Expect(len(cfg.Scanners), "scanners")
		for _, s := range cfg.Scanners {
			if s.ID == "" || len(s.Categories) == 0 || s.Driver == "" || s.Driver != scancatalog.DriverExternal && s.Impl == "" {
				r.Report("invalid scanner entry %+v", s)
			}
		}
	},
}, {
	// Every bundled binary is attributed once with a license and exactly one
	// source; a release-derived artifact carries nothing but its name.
	Name:    "bundled-binaries-are-attributed",
	Anchors: []laws.Anchor{laws.File("licensing/bundled-binaries.yaml")},
	Check: func(r *laws.Run) {
		var catalog struct {
			Binaries []struct {
				Name            string `yaml:"name"`
				Version         string `yaml:"version"`
				License         string `yaml:"license"`
				SourceURL       string `yaml:"source_url"`
				SourcePath      string `yaml:"source_path"`
				LicenseTextFile string `yaml:"license_text_file"`
				Artifact        string `yaml:"artifact"`
			} `yaml:"binaries"`
		}
		r.Must(yaml.Unmarshal(r.Bytes("licensing/bundled-binaries.yaml"), &catalog))
		seen := map[string]bool{}
		for _, b := range catalog.Binaries {
			if b.Name == "" || seen[b.Name] {
				r.Report("binary %q is unnamed or attributed twice", b.Name)
				continue
			}
			seen[b.Name] = true
			if b.Name == "opengrep" || b.Artifact != "" {
				if b.Name != "opengrep" || b.Artifact != "opengrep" || b.Version+b.License+b.SourcePath+b.SourceURL+b.LicenseTextFile != "" {
					r.Report("artifact attribution %s must derive from the selected release", b.Name)
				}
				continue
			}
			if b.Version == "" || b.License == "" || (b.SourceURL == "") == (b.SourcePath == "") {
				r.Report("binary %s needs a version, a license and exactly one source", b.Name)
			}
			if b.SourcePath != "" && (!filepath.IsLocal(b.SourcePath) || filepath.ToSlash(filepath.Clean(b.SourcePath)) != b.SourcePath) {
				r.Report("binary %s source path %q escapes the application resources", b.Name, b.SourcePath)
			}
			if b.LicenseTextFile != "" {
				r.Bytes("licensing/" + b.LicenseTextFile)
			}
		}
		for _, name := range []string{"opengrep", "chrome-headless-shell"} {
			if !seen[name] {
				r.Report("no attribution for %s", name)
			}
		}
	},
}}

func goFileAnchors(dir string, names []string) []laws.Anchor {
	out := make([]laws.Anchor, 0, len(names))
	for _, name := range names {
		out = append(out, laws.File(path.Join(dir, name+".yaml")))
	}
	return out
}
