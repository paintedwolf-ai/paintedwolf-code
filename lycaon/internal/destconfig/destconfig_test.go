package destconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/internal/testutil"
)

func registryWith(hosts ...string) *destconfig.Registry {
	r := destconfig.NewRegistry()
	r.Add(destconfig.Source{Name: "settings", Hosts: func(string) []string { return hosts }})
	return r
}

// Configured cites which source matched a host.
func TestConfiguredNamesItsSource(t *testing.T) {
	r := destconfig.NewRegistry()
	r.Add(destconfig.Source{Name: "mcp_providers", Hosts: func(string) []string {
		return []string{"https://mcp.acme.example/v1"}
	}})
	r.Add(destconfig.Source{Name: "provider_endpoints", Hosts: func(string) []string {
		return []string{"https://api.example.com/v1"}
	}})

	if src, ok := r.Configured("/proj", "mcp.acme.example"); !ok || src != "mcp_providers" {
		t.Fatalf("mcp.acme.example = %q/%v", src, ok)
	}
	if src, ok := r.Configured("/proj", "api.example.com"); !ok || src != "provider_endpoints" {
		t.Fatalf("api.example.com = %q/%v", src, ok)
	}
	if _, ok := r.Configured("/proj", "paste.example"); ok {
		t.Fatal("a host nobody configured must not read as configured")
	}
}

// Sources answer per project.
func TestSourcesAreProjectScoped(t *testing.T) {
	r := destconfig.NewRegistry()
	r.Add(destconfig.Source{Name: "mcp_providers", Hosts: func(dir string) []string {
		if dir == "/a" {
			return []string{"https://mcp.internal"}
		}
		return nil
	}})
	if _, ok := r.Configured("/a", "mcp.internal"); !ok {
		t.Fatal("project /a configured it")
	}
	if _, ok := r.Configured("/b", "mcp.internal"); ok {
		t.Fatal("project /b did not")
	}
}

// Normalize reduces bare hosts and URLs to hostnames.
func TestNormalizeAcceptsTheShapesConfigHolds(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Registry.Example.com/v2/": "registry.example.com",
		"ssh://git@git.internal:2222/x":    "git.internal",
		"api.example.com":                  "api.example.com",
		"http://user:pw@h.example":         "h.example",
		"":                                 "",
		"/local/path":                      "",
	} {
		if got := destconfig.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// An empty registry attests nothing.
func TestEmptyRegistryConfiguresNothing(t *testing.T) {
	if _, ok := destconfig.NewRegistry().Configured("/proj", "example.com"); ok {
		t.Fatal("an unwired registry must not attest a host")
	}
	if _, ok := registryWith().Configured("/proj", "example.com"); ok {
		t.Fatal("a source that reports nothing must not attest a host")
	}
}

// Adding a source invalidates cached lookup results.
func TestAddingASourceInvalidatesTheCache(t *testing.T) {
	r := destconfig.NewRegistry()
	if _, ok := r.Configured("/proj", "late.example"); ok {
		t.Fatal("unexpectedly configured")
	}
	r.Add(destconfig.Source{Name: "mcp_providers", Hosts: func(string) []string {
		return []string{"late.example"}
	}})
	if _, ok := r.Configured("/proj", "late.example"); !ok {
		t.Fatal("a source added after a lookup must take effect")
	}
}

// An unwired registry attests no hosts by default.
func TestNothingIsConfiguredByDefault(t *testing.T) {
	r := destconfig.NewRegistry()
	for _, host := range []string{
		"api.vendor.example", "registry.npmjs.org", "github.com", "pypi.org", "crates.io",
	} {
		if src, ok := r.Configured("/proj", host); ok {
			t.Errorf("%q was attested by %q with no source wired", host, src)
		}
	}
}

func TestGitRemoteHostsForRootsParsesRemotes(t *testing.T) {
	dir1 := t.TempDir()
	gitDir1 := filepath.Join(dir1, ".git")
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(gitDir1, 0o755))
	config1 := `[remote "origin"]
	url = git@github.com:myorg/repo1.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	testutil.FailErr(t, "write config1", os.WriteFile(filepath.Join(gitDir1, "config"), []byte(config1), 0o644))

	dir2 := t.TempDir()
	gitDir2 := filepath.Join(dir2, ".git")
	testutil.FailErr(t, "mkdir .git2", os.MkdirAll(gitDir2, 0o755))
	config2 := `[remote "upstream"]
	url = https://gitlab.internal/other/repo2.git
`
	testutil.FailErr(t, "write config2", os.WriteFile(filepath.Join(gitDir2, "config"), []byte(config2), 0o644))

	hosts := destconfig.GitRemoteHostsForRoots(dir1, dir2)
	if hosts["github.com"] != "git:origin" {
		t.Fatalf("github.com = %q, want git:origin", hosts["github.com"])
	}
	if hosts["gitlab.internal"] != "git:upstream" {
		t.Fatalf("gitlab.internal = %q, want git:upstream", hosts["gitlab.internal"])
	}
}

