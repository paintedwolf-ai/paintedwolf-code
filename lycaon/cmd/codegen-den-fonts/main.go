// Command codegen-den-fonts copies declared faces and generates the font catalog.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/browser/designkit"
	"gopkg.in/yaml.v3"
)

const (
	catalogRel    = "../lycaon-den/fonts.yaml"
	facesRel      = "../lycaon-den/src/fonts/font-faces.generated.css"
	moduleRel     = "../lycaon-den/src/fonts/font-catalog.generated.ts"
	assetDirRel   = "../lycaon-den/public/fonts"
	licenseSubdir = "licenses"

	// The font CSP permits only files served from the app origin.
	assetURLPrefix = "/fonts/"
)

type catalogFamily struct {
	Family      string `yaml:"family"`
	Role        string `yaml:"role"`
	Default     bool   `yaml:"default"`
	Description string `yaml:"description"`
}

type catalogDoc struct {
	CatalogVersion int `yaml:"catalog_version"`
	Fallbacks      struct {
		UI   string `yaml:"ui"`
		Mono string `yaml:"mono"`
	} `yaml:"fallbacks"`
	Families []catalogFamily `yaml:"families"`
}

// resolved pairs a catalog entry with the pack file and licence it ships with.
type resolved struct {
	catalogFamily
	File        string
	LicenseFile string
	Copyright   string
	License     string
}

func main() {
	root := flag.String("root", ".", "repo-relative Go module root")
	check := flag.Bool("check", false, "fail when generated output is stale")
	flag.Parse()

	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "codegen-den-fonts:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	doc, err := loadCatalog(filepath.Join(root, catalogRel))
	if err != nil {
		return err
	}
	families, err := resolveFamilies(doc)
	if err != nil {
		return err
	}

	files := map[string][]byte{
		filepath.Join(root, facesRel):  faceStylesheet(doc, families),
		filepath.Join(root, moduleRel): catalogModule(doc, families),
	}
	assetDir := filepath.Join(root, assetDirRel)
	for _, f := range families {
		face, err := designkit.FontFile(f.File)
		if err != nil {
			return err
		}
		files[filepath.Join(assetDir, f.File)] = face

		licence, err := designkit.LicenseFile(f.LicenseFile)
		if err != nil {
			return err
		}
		files[filepath.Join(assetDir, licenseSubdir, filepath.Base(f.LicenseFile))] = licence
	}
	files[filepath.Join(assetDir, licenseSubdir, "README.md")] = licenceReadme(families)

	if check {
		return verify(files, assetDir)
	}
	return write(files, assetDir)
}

func loadCatalog(path string) (catalogDoc, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- catalog path from the Taskfile
	if err != nil {
		return catalogDoc{}, fmt.Errorf("read %s: %w", path, err)
	}
	var doc catalogDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return catalogDoc{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.CatalogVersion != 1 {
		return catalogDoc{}, fmt.Errorf("%s: unsupported catalog_version %d", path, doc.CatalogVersion)
	}
	if strings.TrimSpace(doc.Fallbacks.UI) == "" || strings.TrimSpace(doc.Fallbacks.Mono) == "" {
		return catalogDoc{}, fmt.Errorf("%s: both fallback stacks are required", path)
	}
	if len(doc.Families) == 0 {
		return catalogDoc{}, fmt.Errorf("%s: no families", path)
	}
	return doc, nil
}

// resolveFamilies rejects catalog entries without matching bundled faces.
func resolveFamilies(doc catalogDoc) ([]resolved, error) {
	seen := map[string]bool{}
	defaults := map[string]string{}
	out := make([]resolved, 0, len(doc.Families))

	for _, entry := range doc.Families {
		if entry.Role != "ui" && entry.Role != "mono" {
			return nil, fmt.Errorf("font %q: role must be ui or mono, got %q", entry.Family, entry.Role)
		}
		if strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("font %q: a description is required — it is the picker's hint", entry.Family)
		}
		if seen[entry.Family] {
			return nil, fmt.Errorf("font %q listed twice", entry.Family)
		}
		seen[entry.Family] = true

		font, ok := designkit.LookupFont(entry.Family)
		if !ok {
			return nil, fmt.Errorf("font %q is not in the designkit pack — add the face there first", entry.Family)
		}
		// CSS family names must match the face despite case-insensitive lookup.
		if font.Family != entry.Family {
			return nil, fmt.Errorf("font %q is spelled %q in the pack", entry.Family, font.Family)
		}
		prov, err := designkit.FontProvenanceFor(entry.Family)
		if err != nil {
			return nil, err
		}
		if entry.Default {
			if prior, dup := defaults[entry.Role]; dup {
				return nil, fmt.Errorf("role %q has two defaults: %s and %s", entry.Role, prior, entry.Family)
			}
			defaults[entry.Role] = entry.Family
		}
		out = append(out, resolved{
			catalogFamily: entry,
			File:          font.File,
			LicenseFile:   prov.LicenseFile,
			Copyright:     prov.Copyright,
			License:       prov.License,
		})
	}

	for _, role := range []string{"ui", "mono"} {
		if defaults[role] == "" {
			return nil, fmt.Errorf("role %q has no default family", role)
		}
	}
	return out, nil
}

func defaultFamily(families []resolved, role string) string {
	for _, f := range families {
		if f.Role == role && f.Default {
			return f.Family
		}
	}
	return ""
}

func fallbackFor(doc catalogDoc, role string) string {
	if role == "mono" {
		return strings.TrimSpace(doc.Fallbacks.Mono)
	}
	return strings.TrimSpace(doc.Fallbacks.UI)
}

// familyStack appends platform fallbacks to a family name.
func familyStack(doc catalogDoc, f resolved) string {
	return quoteFamily(f.Family) + ", " + fallbackFor(doc, f.Role)
}

// stackFor supplies the role's font stack before preferences load.
func stackFor(families []resolved, doc catalogDoc, role string) string {
	for _, f := range families {
		if f.Role == role && f.Default {
			return familyStack(doc, f)
		}
	}
	return fallbackFor(doc, role)
}

func faceStylesheet(doc catalogDoc, families []resolved) []byte {
	var b bytes.Buffer
	b.WriteString("/* Generated by cmd/codegen-den-fonts from lycaon-den/fonts.yaml. */\n")

	// Default tokens keep the first paint stable until font preferences load.
	b.WriteString("\n:root {\n")
	fmt.Fprintf(&b, "  --den-font-ui: %s;\n", stackFor(families, doc, "ui"))
	fmt.Fprintf(&b, "  --den-font-mono: %s;\n", stackFor(families, doc, "mono"))
	b.WriteString("}\n")

	for _, f := range families {
		b.WriteString("\n@font-face {\n")
		fmt.Fprintf(&b, "  font-family: %q;\n", f.Family)
		b.WriteString("  font-style: normal;\n")
		// Each variable font file supplies the full weight range.
		b.WriteString("  font-weight: 100 900;\n")
		b.WriteString("  font-display: block;\n")
		fmt.Fprintf(&b, "  src: url(%q) format(\"woff2\");\n", assetURLPrefix+f.File)
		b.WriteString("}\n")
	}
	return b.Bytes()
}

func catalogModule(doc catalogDoc, families []resolved) []byte {
	var b bytes.Buffer
	b.WriteString("// Generated by cmd/codegen-den-fonts from lycaon-den/fonts.yaml.\n\n")

	b.WriteString("export type DenFontRole = \"ui\" | \"mono\";\n\n")

	b.WriteString("export type DenBundledFont = {\n")
	b.WriteString("  /** CSS family name — also the OFL reserved font name. */\n")
	b.WriteString("  family: string;\n")
	b.WriteString("  role: DenFontRole;\n")
	b.WriteString("  /** One line, shown under the family in Settings. */\n")
	b.WriteString("  description: string;\n")
	b.WriteString("  /** The family followed by the platform fallbacks. */\n")
	b.WriteString("  stack: string;\n")
	b.WriteString("};\n\n")

	b.WriteString("/** Platform fonts supply the system option and missing glyphs. */\n")
	b.WriteString("export const DEN_FONT_FALLBACKS: Readonly<Record<DenFontRole, string>> = {\n")
	fmt.Fprintf(&b, "  ui: %q,\n", strings.TrimSpace(doc.Fallbacks.UI))
	fmt.Fprintf(&b, "  mono: %q,\n", strings.TrimSpace(doc.Fallbacks.Mono))
	b.WriteString("};\n\n")

	b.WriteString("/** The family each role starts from on a fresh install. */\n")
	b.WriteString("export const DEN_FONT_DEFAULTS: Readonly<Record<DenFontRole, string>> = {\n")
	fmt.Fprintf(&b, "  ui: %q,\n", defaultFamily(families, "ui"))
	fmt.Fprintf(&b, "  mono: %q,\n", defaultFamily(families, "mono"))
	b.WriteString("};\n\n")

	b.WriteString("export const DEN_BUNDLED_FONTS: readonly DenBundledFont[] = [\n")
	for _, f := range families {
		b.WriteString("  {\n")
		fmt.Fprintf(&b, "    family: %q,\n", f.Family)
		fmt.Fprintf(&b, "    role: %q,\n", f.Role)
		fmt.Fprintf(&b, "    description: %q,\n", f.Description)
		fmt.Fprintf(&b, "    stack: %q,\n", familyStack(doc, f))
		b.WriteString("  },\n")
	}
	b.WriteString("];\n")
	return b.Bytes()
}

func quoteFamily(family string) string {
	return `"` + strings.ReplaceAll(family, `"`, `\"`) + `"`
}

func licenceReadme(families []resolved) []byte {
	var b bytes.Buffer
	b.WriteString("<!-- Generated by cmd/codegen-den-fonts. Do not edit by hand. -->\n\n")
	b.WriteString("# Bundled font licences\n\n")
	b.WriteString("Painted Wolf Code redistributes these faces inside the app bundle. Each\n")
	b.WriteString("licence below applies to the file named beside it, and ships with it.\n\n")
	b.WriteString("| Family | File | Licence | Copyright |\n")
	b.WriteString("|--------|------|---------|-----------|\n")
	sorted := append([]resolved(nil), families...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Family < sorted[j].Family })
	for _, f := range sorted {
		fmt.Fprintf(&b, "| %s | `%s` | [%s](%s) | %s |\n",
			f.Family, f.File, f.License, filepath.Base(f.LicenseFile), f.Copyright)
	}
	return b.Bytes()
}

func write(files map[string][]byte, assetDir string) error {
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil { // #nosec G306 -- generated artifact
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	// Remove faces and licenses absent from the catalog.
	stale, err := unexpectedAssets(files, assetDir)
	if err != nil {
		return err
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale %s: %w", path, err)
		}
	}
	return nil
}

func verify(files map[string][]byte, assetDir string) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		current, err := os.ReadFile(path) // #nosec G304 -- generated path from the Taskfile
		if err != nil {
			return fmt.Errorf("read %s: %w — run ./task codegen:den-fonts", path, err)
		}
		if !bytes.Equal(current, files[path]) {
			return fmt.Errorf("%s is stale; run ./task codegen:den-fonts", path)
		}
	}
	stale, err := unexpectedAssets(files, assetDir)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		return fmt.Errorf("%s is not in the catalog; run ./task codegen:den-fonts", stale[0])
	}
	return nil
}

// unexpectedAssets lists files under the shipped-font directory that this run
// did not produce.
func unexpectedAssets(files map[string][]byte, assetDir string) ([]string, error) {
	var stale []string
	err := filepath.WalkDir(assetDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, expected := files[path]; !expected {
			stale = append(stale, path)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", assetDir, err)
	}
	sort.Strings(stale)
	return stale, nil
}
