package projectstack

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

type stubRepo struct {
	langs []string
}

func (s stubRepo) Brief(context.Context, string) (*repoinfo.Brief, error) {
	return &repoinfo.Brief{Languages: append([]string(nil), s.langs...), Materialized: true}, nil
}
func (stubRepo) KnownEmpty(context.Context, string) (bool, error) { return false, nil }
func (stubRepo) Warm(string)                                      {}
func (stubRepo) Changed(context.Context, string)                  {}
func (stubRepo) SetOnSettled(func(string))                        {}
func (stubRepo) Close() error                                     { return nil }

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(content), 0o644))
}

func TestDepsFromManifestsAcrossFormats(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.26\n\nrequire (\n\tgithub.com/direct/dep v1.0.0\n\tgithub.com/indirect/dep v1.0.0 // indirect\n)\n")
	writeFile(t, dir, "package.json", `{"dependencies":{"solid-js":"^1.0"},"devDependencies":{"vitest":"^2.0"}}`)
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"app\"\n\n[dependencies]\nserde = \"1\"\n")
	writeFile(t, dir, "pyproject.toml", "[project]\ndependencies = [\"requests>=2.0\", \"uvicorn[standard]==0.30\"]\n")
	writeFile(t, dir, "requirements.txt", "# pinned\nflask==3.0\n-r other.txt\n")
	writeFile(t, dir, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\ngem \"sidekiq\"\n")
	writeFile(t, dir, "composer.json", `{"require":{"php":"^8.2","laravel/framework":"^11.0"},"require-dev":{"phpunit/phpunit":"^11.0"}}`)
	writeFile(t, dir, "cpanfile", "requires 'Moose', '2.0';\non 'test' => sub {\n  requires 'Test::More', '1.0';\n};\n")
	writeFile(t, dir, "META.json", `{"prereqs":{"runtime":{"requires":{"perl":"5.38","DBI":"1.0"}}}}`)

	manifests, err := discoverManifests(context.Background(), dir)
	testutil.FailErr(t, "discover", err)
	deps := depsFromManifests(dir, manifests)
	want := map[string]string{
		"github.com/direct/dep": EcosystemGo,
		"solid-js":              EcosystemNPM,
		"vitest":                EcosystemNPM,
		"serde":                 EcosystemCargo,
		"requests":              EcosystemPyPI,
		"uvicorn":               EcosystemPyPI,
		"flask":                 EcosystemPyPI,
		"rails":                 EcosystemRubyGems,
		"sidekiq":               EcosystemRubyGems,
		"laravel/framework":     EcosystemComposer,
		"phpunit/phpunit":       EcosystemComposer,
		"Moose":                 EcosystemMetaCPAN,
		"Test::More":            EcosystemMetaCPAN,
		"DBI":                   EcosystemMetaCPAN,
	}
	for name, eco := range want {
		if !slices.Contains(deps, Dep{Name: name, Ecosystem: eco}) {
			t.Fatalf("deps = %v missing %s (%s)", deps, name, eco)
		}
	}
	if slices.ContainsFunc(deps, func(d Dep) bool { return d.Name == "github.com/indirect/dep" }) {
		t.Fatalf("deps = %v must exclude indirect go deps", deps)
	}
	if slices.ContainsFunc(deps, func(d Dep) bool { return d.Name == "php" }) {
		t.Fatalf("deps = %v must exclude php runtime from composer", deps)
	}
	if slices.ContainsFunc(deps, func(d Dep) bool { return d.Name == "perl" }) {
		t.Fatalf("deps = %v must exclude the Perl runtime from MetaCPAN dependencies", deps)
	}
}

func TestParseLegacyCPANMetaYAML(t *testing.T) {
	var got []string
	parseCPANMeta([]byte("requires:\n  Moo: '2.0'\nbuild_requires:\n  ExtUtils::MakeMaker: '0'\n"), true, func(name string) {
		got = append(got, name)
	})
	if !slices.Equal(got, []string{"ExtUtils::MakeMaker", "Moo"}) {
		t.Fatalf("legacy META.yml dependencies = %v", got)
	}
}

func TestParseCPANFileSyntaxVariants(t *testing.T) {
	var got []string
	parseCPANFile([]byte("requires 'Moo';\nrequires(\"Path::Tiny\", '0.1');\nrequires 'perl', '5.38';\n"), func(name string) {
		got = append(got, name)
	})
	if !slices.Equal(got, []string{"Moo", "Path::Tiny"}) {
		t.Fatalf("cpanfile dependencies = %v", got)
	}
}

func TestManifestDependenciesDedupeWithinEcosystem(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"Moose":"1"}}`)
	writeFile(t, dir, "cpanfile", "requires 'Moose';\nrequires 'Moose';\n")
	manifests, err := discoverManifests(context.Background(), dir)
	testutil.FailErr(t, "discover", err)
	deps := depsFromManifests(dir, manifests)
	if !slices.Contains(deps, Dep{Name: "Moose", Ecosystem: EcosystemNPM}) ||
		!slices.Contains(deps, Dep{Name: "Moose", Ecosystem: EcosystemMetaCPAN}) {
		t.Fatalf("cross-ecosystem package names collapsed: %v", deps)
	}
	if count := len(deps); count != 2 {
		t.Fatalf("dependencies = %v, want one Moose per ecosystem", deps)
	}
}

func TestDiscoverManifestsMonorepoDepth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module root\n")
	writeFile(t, dir, "lycaon/go.mod", "module example.com/lycaon\n\nrequire github.com/nested/dep v1.0.0\n")
	writeFile(t, dir, "deep/nested/go.mod", "module too.deep\n")

	manifests, err := discoverManifests(context.Background(), dir)
	testutil.FailErr(t, "discover", err)
	if !slices.Contains(manifests, "go.mod") || !slices.Contains(manifests, "lycaon/go.mod") {
		t.Fatalf("manifests = %v want root and lycaon/go.mod", manifests)
	}
	if slices.Contains(manifests, "deep/nested/go.mod") {
		t.Fatalf("manifests = %v must exclude depth > 2", manifests)
	}

	signals, err := Collect(context.Background(), dir, stubRepo{langs: []string{"Go"}})
	testutil.FailErr(t, "collect", err)
	if !slices.Contains(signals.Deps, Dep{Name: "github.com/nested/dep", Ecosystem: EcosystemGo}) {
		t.Fatalf("deps = %v want nested module", signals.Deps)
	}
}

func TestDocURLsFromREADME(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# App\nSee https://docs.example.com/start and https://pkg.go.dev/example.com/lib\n")
	urls := docURLsFromRoot(dir)
	if len(urls) != 2 {
		t.Fatalf("urls = %v want 2 https links", urls)
	}
}

func TestDocURLsSkipLocalhost(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "local http://127.0.0.1/x https://localhost/docs https://docs.example.com\n")
	urls := docURLsFromRoot(dir)
	if len(urls) != 1 || urls[0] != "https://docs.example.com" {
		t.Fatalf("urls = %v want public https only", urls)
	}
}

func TestSeedQueryDepsBeforeLanguages(t *testing.T) {
	q := SeedQuery(StackSignals{
		Languages: []string{"Go", "TypeScript"},
		Deps:      []Dep{{Name: "react", Ecosystem: EcosystemNPM}, {Name: "github.com/foo/bar", Ecosystem: EcosystemGo}},
	})
	if q == "" {
		t.Fatal("want non-empty query")
	}
	if !stringsContainsInOrder(q, "react", "github.com/foo/bar", "Go") {
		t.Fatalf("query = %q want deps before langs", q)
	}
}

func TestSeedQueryOmitsDocProse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "Secret internal codename Zephyr https://docs.example.com\n")
	writeFile(t, dir, "package.json", `{"dependencies":{"widget-lib":"^1.0"}}`)
	signals, err := Collect(context.Background(), dir, stubRepo{langs: []string{"TypeScript"}})
	testutil.FailErr(t, "collect", err)
	q := SeedQuery(signals)
	if stringsContains(q, "Zephyr") || stringsContains(q, "Secret") {
		t.Fatalf("SeedQuery = %q must not include README prose", q)
	}
	if !stringsContains(q, "widget-lib") {
		t.Fatalf("SeedQuery = %q want dep name", q)
	}
}

func TestFingerprintChangesOnManifestEdit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"1"}}`)
	first, err := Collect(context.Background(), dir, stubRepo{})
	testutil.FailErr(t, "collect", err)
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"2"}}`)
	second, err := Collect(context.Background(), dir, stubRepo{})
	testutil.FailErr(t, "collect", err)
	if first.Fingerprint == "" || second.Fingerprint == "" || first.Fingerprint == second.Fingerprint {
		t.Fatalf("fingerprints %q vs %q want change", first.Fingerprint, second.Fingerprint)
	}
}

func TestCollectNilRepoStillFindsDeps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module x\n\nrequire github.com/a/b v1.0.0\n")
	signals, err := Collect(context.Background(), dir, nil)
	testutil.FailErr(t, "collect", err)
	if !slices.Contains(signals.Deps, Dep{Name: "github.com/a/b", Ecosystem: EcosystemGo}) {
		t.Fatalf("deps = %v", signals.Deps)
	}
}

func stringsContains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func stringsContainsInOrder(s string, parts ...string) bool {
	pos := 0
	for _, p := range parts {
		i := strings.Index(s[pos:], p)
		if i < 0 {
			return false
		}
		pos += i + len(p)
	}
	return true
}
