package pkgregistry_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/pkgregistry"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEverySupportedLanguageIsClaimedOnce(t *testing.T) {
	catalog, err := pkgregistry.Bundled()
	if err != nil {
		testutil.FailErr(t, "load bundled catalog", err)
	}
	claimed := map[string]string{}
	for _, r := range catalog.Registries() {
		for _, language := range r.Languages {
			claimed[language] = r.ID
		}
	}
	for _, u := range catalog.Unregistered() {
		for _, language := range u.Languages {
			claimed[language] = "unregistered"
		}
	}
	supported := map[string]bool{}
	for _, language := range filekind.SupportedLanguages() {
		supported[language] = true
		if _, ok := claimed[language]; !ok {
			t.Errorf("supported language %q has no registry and no unregistered reason", language)
		}
	}
	for language, owner := range claimed {
		if !supported[language] {
			t.Errorf("%s names %q, which is not a supported language", owner, language)
		}
	}
}

func TestHostLookupIsExact(t *testing.T) {
	catalog, err := pkgregistry.Bundled()
	if err != nil {
		testutil.FailErr(t, "load bundled catalog", err)
	}
	if r, ok := catalog.ForHost("Registry.NPMJS.org."); !ok || r.ID != "npm" {
		t.Fatalf("registry.npmjs.org resolved to %+v, %v", r, ok)
	}
	for _, host := range []string{"evil.registry.npmjs.org", "npmjs.org", "github.com", "storage.googleapis.com"} {
		if r, ok := catalog.ForHost(host); ok {
			t.Errorf("%s resolved to registry %s", host, r.ID)
		}
	}
}

func TestParseRejectsAmbiguity(t *testing.T) {
	cases := map[string]string{
		"host in two registries": `version: 1
registries:
  - {id: a, title: A, hosts: [x.test], languages: [go]}
  - {id: b, title: B, hosts: [x.test], languages: [rust]}
`,
		"language in two places": `version: 1
registries:
  - {id: a, title: A, hosts: [a.test], languages: [go]}
unregistered:
  - {languages: [go], reason: none}
`,
		"registry without languages": `version: 1
registries:
  - {id: a, title: A, hosts: [a.test]}
`,
		"unregistered without reason": `version: 1
registries:
  - {id: a, title: A, hosts: [a.test], languages: [go]}
unregistered:
  - {languages: [bash]}
`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := pkgregistry.Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "package registry catalog") {
				t.Fatalf("Parse accepted an ambiguous catalog: %v", err)
			}
		})
	}
}
