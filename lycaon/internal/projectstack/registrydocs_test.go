package projectstack

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
)

func TestLoadRegistryDocTemplatesEmbeddedDefaults(t *testing.T) {
	templates := LoadRegistryDocTemplates()
	if len(templates) == 0 {
		t.Fatal("want embedded default templates")
	}
	for _, eco := range []string{EcosystemGo, EcosystemNPM, EcosystemCargo, EcosystemPyPI, EcosystemRubyGems, EcosystemComposer, EcosystemMetaCPAN} {
		if templates[eco] == "" {
			t.Fatalf("templates = %v missing %q", templates, eco)
		}
	}
}

func TestLoadRegistryDocTemplatesFileWinsWholesale(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.StackRegistryDocs: "ecosystems:\n  go: https://docs.internal/{name}\n",
	})
	templates := LoadRegistryDocTemplates()
	if templates[EcosystemGo] != "https://docs.internal/{name}" {
		t.Fatalf("templates = %v want staged catalog", templates)
	}
	if templates[EcosystemNPM] != "" {
		t.Fatalf("templates = %v: the catalog file wins wholesale, npm must be absent", templates)
	}
}

func TestRegistryDocURLsDerivesPerEcosystem(t *testing.T) {
	templates := LoadRegistryDocTemplates()
	urls := RegistryDocURLs(templates, []Dep{
		{Name: "github.com/foo/bar", Ecosystem: EcosystemGo},
		{Name: "solid-js", Ecosystem: EcosystemNPM},
		{Name: "serde", Ecosystem: EcosystemCargo},
		{Name: "unknown-eco-dep", Ecosystem: "conda"},
		{Name: "Moose", Ecosystem: EcosystemMetaCPAN},
	})
	if !slices.Contains(urls, "https://pkg.go.dev/github.com/foo/bar") {
		t.Fatalf("urls = %v want go module path with slashes intact", urls)
	}
	if !slices.Contains(urls, "https://www.npmjs.com/package/solid-js") {
		t.Fatalf("urls = %v want npm page", urls)
	}
	if !slices.Contains(urls, "https://docs.rs/serde") {
		t.Fatalf("urls = %v want docs.rs page", urls)
	}
	if !slices.Contains(urls, "https://metacpan.org/pod/Moose") {
		t.Fatalf("urls = %v want MetaCPAN module page", urls)
	}
	if len(urls) != 4 {
		t.Fatalf("urls = %v: unknown ecosystems must derive nothing", urls)
	}
}

func TestRegistryDocURLsRejectsUnsafeResults(t *testing.T) {
	urls := RegistryDocURLs(map[string]string{EcosystemGo: "http://localhost/{name}"}, []Dep{
		{Name: "x", Ecosystem: EcosystemGo},
	})
	if len(urls) != 0 {
		t.Fatalf("urls = %v want non-https/local templates rejected", urls)
	}
}
