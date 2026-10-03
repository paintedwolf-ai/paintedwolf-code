package hostresources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseCatalogClosedAndValidated(t *testing.T) {
	valid := []byte(`version: 1
resources:
  - id: docker
    family: containers.local
    label: Docker
    category: Containers
    description: Docker runtime.
    surfaces: [process_exec]
    realizations:
      - platforms: [macos, linux]
        discover:
          all:
            - executable: {names: [docker]}
            - path: {kind: socket, paths: ["/var/run/docker.sock"], capture: socket}
        connections:
          - {mode: local_service, transport: unix_socket, target_from: socket}
        write_roots:
          - "{home}/.docker/buildx"
`)
	catalog, err := ParseCatalog(valid, "test")
	testutil.FailErr(t, "parse valid catalog", err)
	if len(catalog.Resources) != 1 || catalog.Resources[0].origin != "test" {
		t.Fatalf("catalog = %#v", catalog)
	}

	invalidCases := [][]byte{
		[]byte("version: 1\nresources:\n  - id: bad_id\n    label: Bad\n    category: Test\n    description: Bad\n    discover: {executable: {names: [bad]}}\n"),
		[]byte("version: 1\nresources:\n  - id: bad\n    label: Bad\n    category: Test\n    description: Bad\n    surprise: true\n    discover: {executable: {names: [bad]}}\n"),
		[]byte("version: 1\nresources:\n  - id: remote\n    label: Remote\n    category: Test\n    description: Remote\n    discover: {loopback_http: {urls: [https://example.com]}}\n"),
	}
	for i, data := range invalidCases {
		if _, err := ParseCatalog(data, "test"); err == nil {
			t.Fatalf("case %d: expected validation failure", i)
		}
	}
}

func TestBundledCatalogLoads(t *testing.T) {
	service, err := NewService(t.TempDir())
	testutil.FailErr(t, "load bundled catalog", err)
	if len(service.defs) < 40 {
		t.Fatalf("bundled host-resource count = %d want broad developer catalog", len(service.defs))
	}
}

func TestDiscoveryExpressionsAndCaptures(t *testing.T) {
	temp := t.TempDir()
	socket := filepath.Join(temp, "docker.sock")
	file, err := os.Create(socket)
	testutil.FailErr(t, "create path", err)
	testutil.FailErr(t, "close path", file.Close())

	env := defaultDiscoveryEnvironment(temp)
	env.homeDir = temp
	env.lookPath = func(name string) (string, error) {
		if name == "docker" {
			return "/usr/local/bin/docker", nil
		}
		return "", os.ErrNotExist
	}
	env.stat = func(path string) (os.FileInfo, error) {
		if path != socket {
			return nil, os.ErrNotExist
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, statErr
		}
		return fakeFileInfo{FileInfo: info, mode: os.ModeSocket}, nil
	}
	def := Definition{
		Realizations: []Realization{{Discover: Expression{All: []Expression{
			{Executable: &ExecutableProbe{Names: []string{"docker"}}},
			{Path: &PathProbe{Paths: []string{"{home}/docker.sock"}, Kind: "socket", Capture: "socket"}},
		}}}},
	}
	got, _ := discover(context.Background(), def, env)
	if got.status != StatusAvailable || got.captures["socket"] != socket {
		t.Fatalf("verdict = %#v", got)
	}
}

func TestPlatformRealizationsAreDisjoint(t *testing.T) {
	t.Parallel()

	data := []byte(`version: 1
resources:
  - id: daemon
    family: developer-services
    label: Daemon
    category: Developer services
    description: Portable daemon.
    surfaces: [process_exec]
    realizations:
      - platforms: [macos, linux]
        discover: {executable: {names: [daemon]}}
      - platforms: [linux, windows]
        discover: {executable: {names: [daemon]}}
`)
	if _, err := ParseCatalog(data, "test"); err == nil || !strings.Contains(err.Error(), "overlapping \"linux\"") {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestWindowsNamedPipeRealization(t *testing.T) {
	t.Parallel()

	pipe := `\\.\pipe\example`
	def := Definition{Realizations: []Realization{
		{Platforms: []string{"macos"}, Discover: Expression{Executable: &ExecutableProbe{Names: []string{"example"}}}},
		{Platforms: []string{"windows"}, Discover: Expression{NamedPipe: &NamedPipeProbe{
			Names: []string{pipe}, Capture: "service",
		}}},
	}}
	env := discoveryEnvironment{
		platform:           "windows",
		namedPipeAvailable: func(name string) (bool, error) { return name == pipe, nil },
	}
	got, realization := discover(context.Background(), def, env)
	if realization == nil || got.status != StatusAvailable || got.captures["service"] != pipe {
		t.Fatalf("realization=%+v verdict=%+v", realization, got)
	}
}

func TestDetectedUnsupportedRouteCannotResolve(t *testing.T) {
	t.Parallel()

	pipe := `\\.\pipe\example`
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	service := &Service{
		defs: []Definition{{
			ID: "daemon", Family: "developer-services", Label: "Daemon",
			Category: "Developer services", Description: "Portable daemon.",
			Surfaces: []ExecutionSurface{SurfaceProcessExec},
			Realizations: []Realization{{
				Platforms: []string{"windows"},
				Discover:  Expression{NamedPipe: &NamedPipeProbe{Names: []string{pipe}, Capture: "service"}},
				Connections: []ConnectionTemplate{{
					Mode: ConnectionLocalService, Transport: LocalServiceNamedPipe, TargetFrom: "service",
				}},
			}},
			origin: "test",
		}},
		env: discoveryEnvironment{
			platform:           "windows",
			namedPipeAvailable: func(string) (bool, error) { return true, nil },
		},
		ttl:               time.Minute,
		now:               func() time.Time { return now },
		connectionSupport: func(string, Connection) bool { return false },
	}
	snapshot := service.Snapshot(context.Background(), false)
	state := snapshot.Resources[0]
	if state.Status != StatusAvailable || state.HostSupport != HostUnsupported ||
		state.Reason != "host_boundary_unsupported" {
		t.Fatalf("state = %+v", state)
	}
	resolved, unmet := service.ResolveForSurfaces(
		context.Background(),
		[]string{"daemon"},
		ProjectContext{},
		[]ExecutionSurface{SurfaceProcessExec},
	)
	if len(resolved) != 0 || !reflect.DeepEqual(unmet, []string{"daemon"}) {
		t.Fatalf("resolved=%+v unmet=%v", resolved, unmet)
	}
}

func TestHostResourceResolutionRequiresAgentSurfaces(t *testing.T) {
	t.Parallel()

	if SurfacesIncludeAll([]ExecutionSurface{}, []ExecutionSurface{SurfaceProcessExec}) {
		t.Fatal("read-only agent satisfied process execution")
	}
	if !SurfacesIncludeAll(
		[]ExecutionSurface{SurfaceProcessExec},
		[]ExecutionSurface{SurfaceProcessExec},
	) {
		t.Fatal("process execution surface was not satisfied")
	}
}

func TestLoopbackHTTPRejectsRedirectsAndFindsService(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer service.Close()
	probe := LoopbackHTTPProbe{URLs: []string{service.URL + "/ready"}, Status: http.StatusNoContent, TimeoutMS: 500}
	env := defaultDiscoveryEnvironment(t.TempDir())
	got := evaluateLoopbackHTTP(context.Background(), probe, env)
	if got.status != StatusAvailable {
		t.Fatalf("verdict = %#v", got)
	}

	redirect := httptest.NewServer(http.RedirectHandler(service.URL+"/ready", http.StatusFound))
	defer redirect.Close()
	got = evaluateLoopbackHTTP(context.Background(), LoopbackHTTPProbe{
		URLs: []string{redirect.URL}, Status: http.StatusNoContent, TimeoutMS: 500,
	}, env)
	if got.status != StatusUnavailable {
		t.Fatalf("redirect verdict = %#v", got)
	}
}

func TestLoopbackDialRejectsNonLoopbackAddress(t *testing.T) {
	dial := loopbackDialContext(100 * time.Millisecond)
	if _, err := dial(context.Background(), "tcp", "192.0.2.1:80"); err == nil {
		t.Fatal("expected non-loopback address rejection")
	}
}

func TestServiceCachesAndResolvesExceptionalConnections(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	lookups := 0
	service := &Service{
		defs: []Definition{{
			ID: "docker", Family: "containers.local", Label: "Docker", Category: "Containers", Description: "Docker.",
			Surfaces: []ExecutionSurface{SurfaceProcessExec},
			Realizations: []Realization{{
				Discover: Expression{Executable: &ExecutableProbe{Names: []string{"docker"}}},
				Connections: []ConnectionTemplate{{
					Mode: ConnectionLocalService, Transport: LocalServiceUnixSocket, Target: "/tmp/docker.sock",
				}},
				WriteRoots: []string{"{home}/.docker/buildx"},
			}},
			origin: "test",
		}},
		env: discoveryEnvironment{
			platform: "macos",
			homeDir:  "/Users/me",
			lookPath: func(string) (string, error) {
				lookups++
				// A path no host has, so the result never depends on what is installed.
				return filepath.Join(absent, "docker"), nil
			},
		},
		ttl:               time.Minute,
		now:               func() time.Time { return now },
		connectionSupport: func(string, Connection) bool { return true },
	}
	first := service.Snapshot(context.Background(), false)
	second := service.Snapshot(context.Background(), false)
	if lookups != 1 || first.CheckedAt != second.CheckedAt {
		t.Fatalf("lookups=%d first=%v second=%v", lookups, first.CheckedAt, second.CheckedAt)
	}
	resolution, unmet := service.ResolveAction(
		context.Background(),
		[]string{"docker", "missing"},
		ProjectContext{},
		[]ExecutionSurface{SurfaceProcessExec},
	)
	if len(unmet) != 1 || unmet[0] != "missing" || len(resolution.Connections.LocalServices) != 1 {
		t.Fatalf("resolution=%#v unmet=%v", resolution, unmet)
	}
	if len(resolution.WriteRoots) != 1 || resolution.WriteRoots[0] != "/Users/me/.docker/buildx" {
		t.Fatalf("write roots = %v", resolution.WriteRoots)
	}
	// Resolving an action looks the executable up again: the snapshot can outlive an
	// app upgrade or runtime switch, and a stale directory still canonicalizes cleanly.
	if lookups != 2 {
		t.Fatalf("resolve lookups=%d, want a fresh lookup at action time", lookups)
	}
	service.Snapshot(context.Background(), true)
	if lookups != 3 {
		t.Fatalf("refresh lookups=%d", lookups)
	}
}

func TestServiceResolvesMultipleAvailableResourcesTogether(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	service := &Service{
		defs: []Definition{
			{
				ID: "first", Family: "developer-services", Label: "First", Category: "Developer services",
				Description: "First resource.", Surfaces: []ExecutionSurface{SurfaceProcessExec},
				Realizations: []Realization{{Discover: Expression{Executable: &ExecutableProbe{Names: []string{"first"}}}}},
				origin:       "test",
			},
			{
				ID: "second", Family: "developer-services", Label: "Second", Category: "Developer services",
				Description: "Second resource.", Surfaces: []ExecutionSurface{SurfaceProcessExec},
				Realizations: []Realization{{Discover: Expression{Executable: &ExecutableProbe{Names: []string{"second"}}}}},
				origin:       "test",
			},
		},
		env: discoveryEnvironment{
			platform: "macos",
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		},
		ttl:               time.Minute,
		now:               func() time.Time { return now },
		connectionSupport: func(string, Connection) bool { return true },
	}
	resolution, unmet := service.ResolveAction(
		context.Background(), []string{"first", "second"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec},
	)
	if len(unmet) != 0 || len(resolution.States) != 2 {
		t.Fatalf("resolution states = %#v, unmet = %v; want both resources", resolution.States, unmet)
	}
}

func TestSnapshotForComposesLivePolicyAndTrustedPromptGuidance(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	access := AccessAsk
	service := &Service{
		defs: []Definition{{
			ID: "local-db", Family: "data-tools", Label: "Local database", Category: "Data", Description: "A local database.",
			Surfaces: []ExecutionSurface{SurfaceProcessExec},
			Realizations: []Realization{{Discover: Expression{
				Executable: &ExecutableProbe{Names: []string{"db"}},
			}}},
			prompt: PromptAdvertise, origin: "test",
		}},
		env: discoveryEnvironment{
			platform: "macos",
			lookPath: func(string) (string, error) { return "/usr/bin/db", nil },
		},
		ttl:               time.Minute,
		now:               func() time.Time { return now },
		connectionSupport: func(string, Connection) bool { return true },
		policyBinder: func(_ context.Context, _ ProjectContext) PolicyEvaluator {
			return func(id, _ string) PolicyDecision {
				if id == "local-db" {
					return PolicyDecision{Access: access, Setting: AccessSetting(access)}
				}
				return PolicyDecision{Access: AccessAllow, Setting: AccessSettingInherit}
			}
		},
	}
	project := t.TempDir()
	testutil.FailErr(t, "create project overlay", os.MkdirAll(filepath.Join(project, settingsoverlay.DirName()), 0o755))
	testutil.FailErr(t, "write project guidance", os.WriteFile(
		filepath.Join(project, settingsoverlay.DirName(), "host-resources.yaml"),
		[]byte("version: 1\nguidance:\n  local-db: avoid\n  missing-resource: advertise\n"), 0o600,
	))

	projectContext := ProjectContext{ID: "project-1", Dir: project}
	first := service.SnapshotFor(context.Background(), false, projectContext, []string{project})
	state := first.Resources[0]
	if state.Access != AccessAsk || state.AccessSetting != AccessSettingAsk || state.Prompt != PromptAvoid {
		t.Fatalf("effective state = %+v", state)
	}
	if first.Fingerprint == "" {
		t.Fatal("effective prompt surface needs a stable fingerprint")
	}
	if len(first.Diagnostics) != 1 || first.Diagnostics[0].Code != "HOST_RESOURCES_PROJECT_GUIDANCE_UNKNOWN" {
		t.Fatalf("unknown project guidance should be diagnosed: %+v", first.Diagnostics)
	}

	access = AccessDeny
	second := service.SnapshotFor(context.Background(), false, projectContext, []string{project})
	if second.Resources[0].Access != AccessDeny || second.Fingerprint == first.Fingerprint {
		t.Fatalf("live policy change did not invalidate the prompt surface: first=%+v second=%+v", first, second)
	}
	resolution, unmet := service.ResolveAction(
		context.Background(),
		[]string{"local-db"},
		projectContext,
		[]ExecutionSurface{SurfaceProcessExec},
	)
	if len(unmet) != 0 || len(resolution.Deny) != 1 || resolution.Deny[0] != "local-db" {
		t.Fatalf("action resolution=%+v unmet=%v", resolution, unmet)
	}
}

func TestSnapshotForBindsPolicyOnce(t *testing.T) {
	binds := 0
	service := &Service{
		defs: []Definition{
			{
				ID: "one", Family: "test", Label: "One", Category: "Test", Description: "One.",
				Surfaces: []ExecutionSurface{SurfaceProcessExec},
				Realizations: []Realization{{Discover: Expression{
					Executable: &ExecutableProbe{Names: []string{"one"}},
				}}},
			},
			{
				ID: "two", Family: "test", Label: "Two", Category: "Test", Description: "Two.",
				Surfaces: []ExecutionSurface{SurfaceProcessExec},
				Realizations: []Realization{{Discover: Expression{
					Executable: &ExecutableProbe{Names: []string{"two"}},
				}}},
			},
		},
		env: discoveryEnvironment{
			platform: "macos",
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		},
		ttl:               time.Minute,
		now:               func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) },
		connectionSupport: func(string, Connection) bool { return true },
		policyBinder: func(_ context.Context, _ ProjectContext) PolicyEvaluator {
			binds++
			return func(string, string) PolicyDecision {
				return PolicyDecision{Access: AccessAllow, Setting: AccessSettingInherit}
			}
		},
	}
	snap := service.SnapshotFor(context.Background(), true, ProjectContext{ID: "p"}, nil)
	if binds != 1 {
		t.Fatalf("policy binder calls = %d, want 1", binds)
	}
	if len(snap.Resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(snap.Resources))
	}
}

func TestUserCatalogIsAdditive(t *testing.T) {
	if _, err := mergeDefinitions(
		[]Definition{{ID: "docker"}},
		[]Definition{{ID: "docker"}},
	); err == nil {
		t.Fatal("expected replacement refusal")
	}
}

func TestInvalidUserCatalogIsReportedWithoutDisablingBuiltins(t *testing.T) {
	// The built-in catalog ships in the binary, so a one-resource stand-in is staged
	// as bundled config. Only replaces the real catalog so the counts below are exact.
	configtest.Only(t, map[config.Rel]string{config.HostResources: `version: 1
resources:
  - id: test
    family: test
    label: Test
    category: Test
    description: Built-in host resource.
    surfaces: [process_exec]
    realizations:
      - discover: {path: {kind: file, paths: ["/dev/null"]}}
`})
	configDir := t.TempDir()
	testutil.FailErr(t, "write invalid user catalog", os.WriteFile(
		UserCatalogPath(configDir), []byte("version: 1\nresources: invalid\n"), 0o600,
	))

	service, err := NewService(configDir)
	testutil.FailErr(t, "new service", err)
	snapshot := service.Snapshot(context.Background(), false)
	if len(snapshot.Resources) != 1 || len(snapshot.Diagnostics) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Diagnostics[0].Code != "HOST_RESOURCES_USER_CATALOG_INVALID" {
		t.Fatalf("diagnostic = %#v", snapshot.Diagnostics[0])
	}
	testutil.FailErr(t, "replace user catalog", os.WriteFile(
		UserCatalogPath(configDir), []byte(`version: 1
resources:
  - id: user-test
    family: test
    label: User test
    category: Test
    description: User host resource.
    surfaces: [process_exec]
    realizations:
      - discover: {path: {kind: file, paths: ["/dev/null"]}}
`), 0o600,
	))
	snapshot = service.Snapshot(context.Background(), true)
	if len(snapshot.Resources) != 2 || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("refreshed snapshot = %#v", snapshot)
	}
}

func TestParseRequirementGroups(t *testing.T) {
	groups, err := ParseRequirementGroups("terraform|opentofu, docker")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %#v", groups)
	}
	if groups[0].String() != "docker" || groups[1].String() != "opentofu|terraform" {
		t.Fatalf("canonical groups = %q, %q", groups[0], groups[1])
	}

	if _, err := ParseRequirementGroups("docker|"); err == nil {
		t.Fatal("empty alternative must fail")
	}
	if _, err := ParseRequirementGroups("Bad_Id"); err == nil {
		t.Fatal("invalid id must fail")
	}
	if _, err := ParseRequirementGroups("   "); err != nil {
		t.Fatalf("blank metadata is empty, not an error: %v", err)
	}
	deduped, err := ParseRequirementGroups("docker|docker, docker|docker")
	if err != nil || len(deduped) != 1 || deduped[0].String() != "docker" {
		t.Fatalf("dedupe = %#v err=%v", deduped, err)
	}

	// The flat exec-plane form is the same grammar constrained to exact ids:
	// alternatives are rejected, single-id entries parse identically.
	if _, err := ParseRequirements("docker|podman"); err == nil {
		t.Fatal("exec-plane form must reject alternatives")
	}
	flat, err := ParseRequirements("terraform, docker, docker")
	if err != nil || !reflect.DeepEqual(flat, []string{"docker", "terraform"}) {
		t.Fatalf("flat parse = %v err=%v", flat, err)
	}
}

func TestExecutableProbeUnexpectedErrorIsUnknown(t *testing.T) {
	got := evaluateExecutable(ExecutableProbe{Names: []string{"tool"}}, discoveryEnvironment{
		lookPath: func(string) (string, error) { return "", errors.New("broken PATH") },
	})
	if got.status != StatusUnknown {
		t.Fatalf("verdict = %#v", got)
	}
}

func TestEnvironmentProbeUsesPresenceWithoutCapturingValues(t *testing.T) {
	got := evaluateEnvironment(EnvironmentProbe{Names: []string{"EMPTY", "SECRET"}}, discoveryEnvironment{
		lookupEnv: func(name string) (string, bool) {
			if name == "EMPTY" {
				return "", true
			}
			return "sensitive-value", name == "SECRET"
		},
	})
	if got.status != StatusAvailable || len(got.captures) != 0 {
		t.Fatalf("verdict = %#v, want available without captured values", got)
	}
}

type fakeFileInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (f fakeFileInfo) Mode() os.FileMode { return f.mode }

func TestCatalogPathMatchesTheFileReloaded(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{config.HostResources: `version: 1
resources:
  - id: builtin
    family: test
    label: Built-in
    category: Test
    description: Built-in fixture resource.
    surfaces: [process_exec]
    realizations:
      - discover: {path: {kind: directory, paths: ["{home}"]}}
`})
	for _, dev := range []string{"0", "1"} {
		t.Run("development="+dev, func(t *testing.T) {
			t.Setenv("LYCAON_DEV", dev)
			t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "other config"))
			configDir := filepath.Join(t.TempDir(), "resource settings café")
			testutil.FailErr(t, "create config directory", os.MkdirAll(configDir, 0o700))
			service, err := NewService(configDir)
			testutil.FailErr(t, "new service", err)
			snapshot := service.Snapshot(t.Context(), false)
			if snapshot.UserCatalogPath != UserCatalogPath(configDir) {
				t.Fatalf("advertised catalog = %q want %q", snapshot.UserCatalogPath, UserCatalogPath(configDir))
			}
			testutil.FailErr(t, "write advertised catalog", os.WriteFile(snapshot.UserCatalogPath, []byte(`version: 1
resources:
  - id: displayed-catalog
    family: test
    label: Displayed catalog
    category: Test
    description: Resource from the displayed catalog path.
    surfaces: [process_exec]
    realizations:
      - discover: {path: {kind: directory, paths: ["{home}"]}}
`), 0o600))
			snapshot = service.Snapshot(t.Context(), true)
			found := false
			for _, resource := range snapshot.Resources {
				found = found || resource.ID == "displayed-catalog"
			}
			if len(snapshot.Diagnostics) != 0 || len(snapshot.Resources) != 2 || !found {
				t.Fatalf("catalog written at displayed path was not loaded: %+v", snapshot)
			}
		})
	}
}
