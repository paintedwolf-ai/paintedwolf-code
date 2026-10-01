package packageexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogCoversHighRiskPackageActions(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	tests := []struct {
		command string
		manager string
		name    string
	}{
		{"npx create-vite@6.1.0 demo", "npm", "create-vite"},
		{"npm install unclaimed-company-tool", "npm", "unclaimed-company-tool"},
		{"npm exec --package=@scope/cli@2.0.0 -- cli", "npm", "@scope/cli"},
		{"pnpm dlx tsx@4.19.0 script.ts", "pnpm", "tsx"},
		{"yarn dlx create-next-app@15.2.0 demo", "yarn", "create-next-app"},
		{"bunx cowsay@1.6.0 hello", "bun", "cowsay"},
		{"pipx run black==25.1.0 .", "pipx", "black"},
		{"pip install unclaimed-company-tool", "pip", "unclaimed-company-tool"},
		{"python -m pip install unclaimed-company-tool", "pip", "unclaimed-company-tool"},
		{"uv tool run ruff==0.11.0 check .", "uv", "ruff"},
		{"uv run --with httpx==0.28.1 script.py", "uv", "httpx"},
		{"uv add ruff==0.11.0", "uv", "ruff"},
		{"cargo install ripgrep --version 14.1.1", "cargo", "ripgrep"},
		{"go install golang.org/x/tools/gopls@v0.18.1", "go", "golang.org/x/tools/gopls"},
		{"go run golang.org/x/tools/cmd/stringer@v0.30.0", "go", "golang.org/x/tools/cmd/stringer"},
		{"dotnet tool install dotnetsay --version 2.1.7", "dotnet", "dotnetsay"},
		{"gem exec --version 0.2.0 rake", "gem", "rake"},
		{"mvn org.codehaus.mojo:exec-maven-plugin:3.5.0:java", "maven", "org.codehaus.mojo:exec-maven-plugin"},
		{"composer create-project laravel/laravel:^12 app", "composer", "laravel/laravel:^12"},
		{"env NODE_ENV=test npx create-vite@6.1.0 demo", "npm", "create-vite"},
		{`bash -lc "uvx --from ruff==0.11.0 ruff check ."`, "uv", "ruff"},
		{"command pnpm dlx tsx@4.19.0 script.ts", "pnpm", "tsx"},
		{"uvx --from ruff==0.11.0 ruff check .", "uv", "ruff"},
		{"pipx run --spec black==25.1.0 black .", "pipx", "black"},
		{"cargo add serde --features derive", "cargo", "serde"},
		{"go get golang.org/x/sync@v0.10.0", "go", "golang.org/x/sync"},
		{"dotnet add package Newtonsoft.Json --version 13.0.3", "dotnet", "Newtonsoft.Json"},
		{"dotnet new install Avalonia.Templates", "dotnet", "Avalonia.Templates"},
		{"composer global require laravel/installer", "composer", "laravel/installer"},
		{"cs install metals", "coursier", "metals"},
		{"cs launch com.lihaoyi:ammonite_2.13:3.0.0", "coursier", "com.lihaoyi:ammonite_2.13:3.0.0"},
		{"dart pub add http", "pub", "http"},
		{"dart pub global activate very_good_cli", "pub", "very_good_cli"},
		{"flutter pub add provider", "pub", "provider"},
		{"mix archive.install hex phx_new", "hex", "phx_new"},
		{"cpanm Mojolicious", "cpan", "Mojolicious"},
		{"cpan -i Moose", "cpan", "Moose"},
		{"luarocks install luasocket 3.1.0-1", "luarocks", "luasocket"},
		{"opam install dune", "opam", "dune"},
		{"conan install --requires=zlib/1.3.1 --build=missing", "conan", "zlib/1.3.1"},
		{"scarb add alexandria_math", "scarb", "alexandria_math"},
		{"Install-Module -Name Pester -Scope CurrentUser", "powershell-gallery", "Pester"},
		{`pwsh -Command "Install-PSResource PSScriptAnalyzer"`, "powershell-gallery", "PSScriptAnalyzer"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			service := offlineService(cat)
			execution, err := service.Preflight(t.Context(), map[string]any{"command": test.command}, "")
			testutil.FailErr(t, "preflight package command", err)
			if execution == nil || execution.Manager != test.manager || len(execution.Packages) == 0 {
				t.Fatalf("execution = %#v", execution)
			}
			if execution.Packages[0].Name != test.name {
				t.Fatalf("package = %q, want %q", execution.Packages[0].Name, test.name)
			}
		})
	}
}

func TestCatalogLeavesOrdinaryDependencyWorkQuiet(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	service := offlineService(cat)
	for _, command := range []string{
		"npm ci", "npm install", "pnpm install --frozen-lockfile",
		"pip install -r requirements.txt", "uv sync --frozen", "bundle install",
		"cargo build --locked", "go mod download", "dotnet restore", "mvn package",
		"go run ./cmd/local-tool", "uv run script.py",
		"go get ./...", "go get -u all", "cpanm --installdeps .", "opam install . --deps-only",
		"dart pub get", "mix deps.get", "luarocks make", "conan install .", "scarb build",
		"dotnet add reference ../Lib/Lib.csproj",
	} {
		execution, preflightErr := service.Preflight(t.Context(), map[string]any{"command": command}, "")
		testutil.FailErr(t, "preflight ordinary dependency command", preflightErr)
		if execution != nil {
			t.Errorf("%q classified as %#v", command, execution)
		}
	}
}

func TestCampaignStyleUnclaimedInstallStillGetsTheBoundary(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	service := offlineService(cat)
	for _, command := range []string{
		"pip install internal-tool-that-does-not-exist",
		"npm install internal-tool-that-does-not-exist",
		"npx internal-tool-that-does-not-exist",
	} {
		execution, preflightErr := service.Preflight(t.Context(), map[string]any{"command": command}, "")
		testutil.FailErr(t, "preflight unclaimed package command", preflightErr)
		if execution == nil {
			t.Fatalf("%q boundary = %#v", command, execution)
		}
		if len(execution.Packages) != 1 || execution.Packages[0].Status != IdentityUnavailable {
			t.Fatalf("%q identity = %#v", command, execution.Packages)
		}
	}
}

func TestCatalogAddsOnlyTheHostFromAnExplicitRegistry(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	execution, ok := cat.classify([]exec.Stage{{
		Name: "npx",
		Args: []string{"--registry", "https://token@example.registry.test/npm/", "create-vite@6.1.0"},
	}})
	if !ok {
		t.Fatal("custom-registry package action was not classified")
	}
	if !slices.Contains(execution.AllowedHosts, "example.registry.test") {
		t.Fatalf("registry hosts = %v", execution.AllowedHosts)
	}
	for _, host := range execution.AllowedHosts {
		if strings.Contains(host, "token") || strings.Contains(host, "/") {
			t.Fatalf("registry host retained credentials or path: %q", host)
		}
	}
}

func TestTerminalInputClassificationRequiresFreshBoundary(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	service := offlineService(cat)
	execution, ok := service.ClassifyTerminalInput("npx create-vite app{Enter}")
	if !ok || execution == nil || execution.Manager != "npm" {
		t.Fatalf("terminal classification = %#v, %v", execution, ok)
	}
	if ordinary, matched := service.ClassifyTerminalInput("git status{Enter}"); matched || ordinary != nil {
		t.Fatalf("ordinary terminal input classified = %#v, %v", ordinary, matched)
	}
}

func TestRegistryPreflightCarriesAgeSourceAndAttestation(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"versionKey":{"system":"NPM","name":"create-vite","version":"6.1.0"},` +
			`"publishedAt":"2026-08-20T00:00:00Z","registries":["https://registry.npmjs.org"],` +
			`"links":[{"label":"SOURCE_REPO","url":"https://github.com/vitejs/vite"}],` +
			`"attestations":[{"verified":true,"sourceRepository":"https://github.com/vitejs/vite"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	service := &Service{catalog: cat, client: client, now: func() time.Time {
		return time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	}}
	execution, err := service.Preflight(context.Background(), map[string]any{"command": "npx create-vite@6.1.0 demo"}, "")
	testutil.FailErr(t, "preflight resolved package", err)
	pkg := execution.Packages[0]
	if pkg.Status != IdentityResolved || pkg.ResolvedVersion != "6.1.0" || pkg.AgeDays == nil || *pkg.AgeDays != 7 {
		t.Fatalf("package identity = %#v", pkg)
	}
	if pkg.SourceRepository == "" || !pkg.VerifiedAttestation {
		t.Fatalf("package source identity = %#v", pkg)
	}
}

func TestRegistryPreflightDistinguishesNotFoundFromUnavailable(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package execution catalog", err)
	service := &Service{
		catalog: cat,
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
				Header:     make(http.Header),
			}, nil
		})},
		now: time.Now,
	}
	execution, preflightErr := service.Preflight(
		t.Context(), map[string]any{"command": "npm install unclaimed-company-tool"}, "",
	)
	testutil.FailErr(t, "preflight not-found package", preflightErr)
	if execution == nil || len(execution.Packages) != 1 || execution.Packages[0].Status != IdentityNotFound {
		t.Fatalf("not-found identity = %#v", execution)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func offlineService(cat *catalog) *Service {
	return &Service{
		catalog: cat,
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline fixture")
		})},
		now: time.Now,
	}
}

func TestPackageOptionValuesNeverBecomeCoordinates(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package catalog", err)
	for executable, rules := range cat.byExecutable {
		for index, candidate := range rules {
			rule := candidate.rule
			for _, flag := range rule.ValueFlags {
				if slices.Contains(rule.VersionFlags, flag) || slices.Contains(rule.PackageFlags, flag) || slices.Contains(rule.RegistryFlags, flag) {
					continue
				}
				for _, inline := range []bool{false, true} {
					for _, before := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%d/%s/inline=%v/before=%v", executable, index, flag, inline, before), func(t *testing.T) {
							coordinate := "example-package@1.2.3"
							switch rule.PackageMode {
							case "maven_plugin_coordinates":
								coordinate = "org.example:plugin:1.2.3:run"
							case "module_paths":
								coordinate = "example.com/package@v1.2.3"
							}
							pkgArgs := []string{coordinate}
							if rule.PackageMode == "flag_only" {
								pkgArgs = []string{rule.PackageFlags[0], coordinate}
							}
							option := []string{flag, "/tmp/option-value"}
							if inline {
								option = []string{flag + "=/tmp/option-value"}
							}
							args := append([]string(nil), rule.Verbs...)
							if before {
								args = append(args, option...)
							}
							args = append(args, pkgArgs...)
							if !before {
								args = append(args, option...)
							}
							got, ok := classifyRule(candidate, args)
							if !ok || !slices.Equal(got.packages, []string{coordinate}) {
								t.Fatalf("%v packages=%v matched=%v", args, got.packages, ok)
							}
						})
					}
				}
			}
		}
	}
}

func TestPipCacheAndCertificateOptionsPreservePackageIdentity(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package catalog", err)
	service := offlineService(cat)
	for _, executable := range []string{"pip", "pip3", "python -m pip", "python3 -m pip", "package-check/.venv/bin/pip"} {
		for _, options := range []string{
			"--cache-dir /tmp/package-cache --cert /tmp/cacert.pem --timeout 30",
			"--cache-dir=/tmp/package-cache --cert=/tmp/cacert.pem --timeout=30",
		} {
			for _, command := range []string{executable + " install " + options + " colorama==0.4.6", executable + " install colorama==0.4.6 " + options} {
				got, preflightErr := service.Preflight(t.Context(), map[string]any{"command": command}, "")
				testutil.FailErr(t, "classify pip options", preflightErr)
				if got == nil || len(got.Packages) != 1 || got.Packages[0].Name != "colorama" || got.Packages[0].RequestedVersion != "0.4.6" {
					t.Fatalf("%q execution=%+v", command, got)
				}
			}
		}
	}
}

func TestBunRuntimeFlagDoesNotConsumePackage(t *testing.T) {
	cat, err := loadCatalog()
	testutil.FailErr(t, "load package catalog", err)
	service := offlineService(cat)
	for _, command := range []string{"bunx --bun cowsay@1.6.0 hello", "bun x --bun cowsay@1.6.0 hello", "bunx -p cowsay@1.6.0 cowsay hello", "bun x -p cowsay@1.6.0 cowsay hello"} {
		got, preflightErr := service.Preflight(t.Context(), map[string]any{"command": command}, "")
		testutil.FailErr(t, "classify bun runtime option", preflightErr)
		if got == nil || len(got.Packages) != 1 || got.Packages[0].Name != "cowsay" {
			t.Fatalf("%q execution=%+v", command, got)
		}
	}
}
