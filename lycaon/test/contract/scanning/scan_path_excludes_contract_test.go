package contract

import (
	"path"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// genericModeRulePacks names language-agnostic rule groups.
var genericModeRulePacks = map[string]bool{
	"perl":       true,
	"powershell": true,
	"groovy":     true,
}

// TestScanExcludesCoverEverySupportedLanguage checks floor coverage.
func TestScanExcludesCoverEverySupportedLanguage(t *testing.T) {
	t.Parallel()
	cfg, err := rules.LoadPathExcludes()
	contractcheck.FailErr(t, "load bundled scan-excludes", err)

	covered := map[string]string{}
	for _, lang := range cfg.CoveredLanguages() {
		covered[lang] = "group"
	}
	for _, entry := range cfg.NoVendorDir {
		lang := strings.TrimSpace(entry.Language)
		if strings.TrimSpace(entry.Why) == "" {
			t.Errorf("no_vendor_dir %q needs a why", lang)
		}
		if prev, dup := covered[lang]; dup {
			t.Errorf("language %q classified twice (%s and no_vendor_dir)", lang, prev)
		}
		covered[lang] = "no_vendor_dir"
	}

	for _, lang := range filekind.SupportedLanguages() {
		if covered[lang] == "" {
			t.Errorf("supported language %q missing from scan-excludes.yaml — add it to a group or no_vendor_dir", lang)
		}
	}
	for lang := range covered {
		if genericModeRulePacks[lang] {
			continue
		}
		if !supportedLanguage(lang) {
			t.Errorf("scan-excludes.yaml names %q, which is not a supported language", lang)
		}
	}
}

func supportedLanguage(lang string) bool {
	for _, l := range filekind.SupportedLanguages() {
		if l == lang {
			return true
		}
	}
	return false
}

// TestScanExcludesAmbiguousNamesStayInScope protects cross-ecosystem names.
func TestScanExcludesAmbiguousNamesStayInScope(t *testing.T) {
	t.Parallel()
	cfg, err := rules.LoadPathExcludes()
	contractcheck.FailErr(t, "load bundled scan-excludes", err)

	excluded := map[string]bool{}
	for _, p := range cfg.Patterns() {
		excluded[p] = true
	}
	for _, entry := range cfg.NotExcluded {
		if excluded[entry.Pattern] {
			t.Errorf("%q is documented as not_excluded but appears in a group", entry.Pattern)
		}
		if strings.TrimSpace(entry.SourceFor) == "" || strings.TrimSpace(entry.DependencyFor) == "" {
			t.Errorf("not_excluded %q needs both dependency_for and source_for", entry.Pattern)
		}
	}
	for _, name := range []string{"packages", "bin", "lib", "deps"} {
		if excluded[name] {
			t.Errorf("%q excludes first-party source in at least one supported ecosystem", name)
		}
	}
}

// TestScanScopeNoteDerivesFromCatalog checks declared scanner scope.
func TestScanScopeNoteDerivesFromCatalog(t *testing.T) {
	t.Parallel()
	cfg, err := rules.LoadPathExcludes()
	contractcheck.FailErr(t, "load bundled scan-excludes", err)
	floor := scan.SASTFloor{Leads: cfg.GroupLeads(), Patterns: cfg.Patterns()}
	excludesRef := path.Base(config.ScanExcludes.String())

	note := scan.ScanScopeFor(scancatalog.ScannerContract{Scope: scancatalog.ScopeSourceHostFloor}, floor)
	if !strings.Contains(note, excludesRef) {
		t.Errorf("bundled SAST note must point at the catalog: %q", note)
	}
	for _, lead := range cfg.GroupLeads()[:2] {
		if !strings.Contains(note, lead) {
			t.Errorf("note %q missing lead pattern %q", note, lead)
		}
	}

	external := scan.ScanScopeFor(scancatalog.ScannerContract{Scope: scancatalog.ScopeSourceDriver}, floor)
	if strings.Contains(external, excludesRef) {
		t.Errorf("only the bundled driver applies the floor; got %q", external)
	}
}
