package detectionpack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectEffectReachDefaultsUnproven(t *testing.T) {
	t.Parallel()
	if got := ProjectEffectReach(""); got != EffectReachUnproven {
		t.Fatalf("empty = %q", got)
	}
	if got := ProjectEffectReach("bogus"); got != EffectReachUnproven {
		t.Fatalf("bogus = %q", got)
	}
	if got := ProjectEffectReach("LOCAL"); got != EffectReachLocal {
		t.Fatalf("LOCAL = %q", got)
	}
}

func TestNewEventProjectsEffectReach(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionObservation{
		Tool:        "command",
		CommandLine: "cargo publish",
		Boundary:    hitl.Contained{FSJailed: true, Egress: "proxy"},
		EffectReach: EffectReachLocal,
	})
	if ev.EffectReach != EffectReachLocal {
		t.Fatalf("EffectReach=%q", ev.EffectReach)
	}
	v, ok := ev.lookup("EffectReach")
	if !ok || v != EffectReachLocal {
		t.Fatalf("lookup EffectReach = %v ok=%v", v, ok)
	}
}

func TestResolveEffectReachStructuredEndpoint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "loopback", args: map[string]any{"endpoint_url": "http://127.0.0.1:4566"}, want: EffectReachLocal},
		{name: "nested_loopback", args: map[string]any{"config": map[string]any{"endpoint": "http://localhost:8080"}}, want: EffectReachLocal},
		{name: "remote", args: map[string]any{"base_url": "https://api.example.test"}, want: EffectReachRemote},
		{name: "no_endpoint", args: map[string]any{"action": "iam:CreateAccessKey"}, want: EffectReachUnproven},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveEffectReach("mcp_aws_call", tc.args, "/project", []string{"/project"}); got != tc.want {
				t.Fatalf("reach=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveEffectReachCargo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cargoDir := filepath.Join(dir, ".cargo")
	testutil.FailErr(t, "mkdir", os.MkdirAll(cargoDir, 0o755))
	cfg := `[registries]
local-registry = { index = "http://127.0.0.1:8081/git" }
evil = { index = "https://crates.io/api/v1/crates" }
`
	testutil.FailErr(t, "write config", os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte(cfg), 0o600))

	roots := []string{dir}
	args := map[string]any{"command": "cargo publish --registry local-registry"}
	if got := ResolveEffectReach("command", args, dir, roots); got != EffectReachLocal {
		t.Fatalf("local-registry = %q, want local", got)
	}
	args = map[string]any{"command": "cargo publish"}
	if got := ResolveEffectReach("command", args, dir, roots); got != EffectReachRemote {
		t.Fatalf("default crates.io = %q, want remote", got)
	}
	args = map[string]any{"command": "cargo publish --registry missing"}
	if got := ResolveEffectReach("command", args, dir, roots); got != EffectReachUnproven {
		t.Fatalf("missing registry = %q, want unproven", got)
	}
	args = map[string]any{"command": "cargo publish --registry evil"}
	if got := ResolveEffectReach("command", args, dir, roots); got != EffectReachRemote {
		t.Fatalf("evil alias = %q, want remote", got)
	}
}

func TestResolveEffectReachCargoFindsProjectConfigFromNestedCrate(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	cargoDir := filepath.Join(project, ".cargo")
	crateDir := filepath.Join(project, "crates", "greeter")
	testutil.FailErr(t, "mkdir cargo config", os.MkdirAll(cargoDir, 0o755))
	testutil.FailErr(t, "mkdir nested crate", os.MkdirAll(crateDir, 0o755))
	testutil.FailErr(t, "write config", os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte(`
[registries.local-registry]
index = "http://127.0.0.1:8872/git"
`), 0o600))
	args := map[string]any{
		"command": "cargo publish --registry local-registry",
		"cwd":     filepath.Join("crates", "greeter"),
	}
	if got := ResolveEffectReach("command", args, project, []string{project}); got != EffectReachLocal {
		t.Fatalf("nested crate registry reach = %q, want local", got)
	}
}

func TestPerProcessReachSilencesOnlyContainedCleanupStage(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	source := NewGateSource(NewMatcher(cat))
	project := resolvedTempDir(t)
	action := hitl.ProposedAction{
		Tool: "command", ProjectDir: project, SessionID: "chat",
		Args: map[string]any{"command": "rm -rf target Cargo.lock && cargo build"},
		Contained: hitl.Contained{
			FSJailed: true, Egress: hitl.ContainedEgressProxy,
			Roots: []string{project}, WriteRoots: []string{project},
		},
	}
	if match, ok := source.MatchAction(action, "balanced"); ok {
		t.Fatalf("contained cleanup stage raised %s/%s", match.PackID, match.RuleID)
	}

	outside := resolvedTempDir(t)
	action.Args = map[string]any{"command": "rm -rf " + outside + " && cargo build"}
	if match, ok := source.MatchAction(action, "balanced"); !ok || match.PackID != "command-destructive" {
		t.Fatalf("outside cleanup match = %+v ok=%v", match, ok)
	}
}

func TestResolveEffectReachCargoHomeEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	home := filepath.Join(dir, "cargo-home")
	testutil.FailErr(t, "mkdir", os.MkdirAll(home, 0o755))
	cfg := `[registries]
local-registry = { index = "http://localhost:8081/git" }
`
	testutil.FailErr(t, "write config", os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600))
	args := map[string]any{
		"command": "cargo publish --registry local-registry",
		"env":     map[string]any{"CARGO_HOME": home},
	}
	if got := ResolveEffectReach("command", args, dir, []string{dir}); got != EffectReachLocal {
		t.Fatalf("CARGO_HOME registry = %q, want local", got)
	}
}

func TestResolveEffectReachNpm(t *testing.T) {
	t.Parallel()
	args := map[string]any{"command": "npm publish --registry http://127.0.0.1:4873"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("npm local registry = %q", got)
	}
	args = map[string]any{"command": "npm unpublish acme@1.0.0 --registry http://127.0.0.1:4873"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("npm unpublish local = %q", got)
	}
	args = map[string]any{"command": "npm publish"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachRemote {
		t.Fatalf("npm default = %q", got)
	}
	dir := t.TempDir()
	testutil.FailErr(t, "npmrc", os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("registry=http://127.0.0.1:4873\n"), 0o600))
	args = map[string]any{"command": "npm publish"}
	if got := ResolveEffectReach("command", args, dir, []string{dir}); got != EffectReachLocal {
		t.Fatalf("npmrc local = %q", got)
	}
}

func TestResolveEffectReachPypi(t *testing.T) {
	t.Parallel()
	args := map[string]any{"command": "twine upload --repository-url http://127.0.0.1:8080/ dist/*"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("twine local = %q", got)
	}
	args = map[string]any{"command": "poetry publish"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachRemote {
		t.Fatalf("poetry default = %q", got)
	}
}

func TestResolveEffectReachLocalStack(t *testing.T) {
	t.Parallel()
	args := map[string]any{"command": "aws --endpoint-url=http://localhost:4566 s3 rb s3://b"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("endpoint-url localhost = %q", got)
	}
	args = map[string]any{"command": "aws --endpoint-url=http://127.0.0.1:4566 s3 rb s3://b"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("endpoint-url loopback = %q", got)
	}
	args = map[string]any{
		"command": "aws s3 rb s3://b",
		"env":     map[string]any{"AWS_ENDPOINT_URL": "http://localhost:4566"},
	}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("AWS_ENDPOINT_URL = %q", got)
	}
	args = map[string]any{"command": "aws s3 rb s3://b"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachUnproven {
		t.Fatalf("plain aws = %q, want unproven", got)
	}
}

// Program names do not establish endpoints.
func TestResolveEffectReachProgramNameProvesNothing(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"awslocal s3 rb s3://b",
		"./awslocal s3 rb s3://b",
		"gcloudlocal storage rm gs://b",
	} {
		args := map[string]any{"command": cmd}
		if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachUnproven {
			t.Fatalf("%q = %q, want unproven: a program name is not a destination", cmd, got)
		}
	}
	// An explicit endpoint establishes local reach.
	args := map[string]any{
		"command": "awslocal s3 rb s3://b",
		"env":     map[string]any{"AWS_ENDPOINT_URL": "http://localhost:4566"},
	}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("declared loopback endpoint = %q, want local", got)
	}
}

func TestResolveEffectReachRegistryRedirect(t *testing.T) {
	t.Parallel()
	args := map[string]any{"command": "npm config set registry http://127.0.0.1:4873"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("npm config set local = %q", got)
	}
	args = map[string]any{"command": "pip install acme --index-url http://localhost:3141/simple"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("pip index-url local = %q", got)
	}
	args = map[string]any{"command": "npm config set registry https://registry.npmjs.org"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachRemote {
		t.Fatalf("npm config set remote = %q", got)
	}
}

func TestResolveEffectReachDockerPush(t *testing.T) {
	t.Parallel()
	args := map[string]any{"command": "docker push localhost:5000/api:1.4.0"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("localhost push = %q", got)
	}
	args = map[string]any{"command": "docker push 127.0.0.1:5000/api:1.4.0"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachLocal {
		t.Fatalf("loopback push = %q", got)
	}
	args = map[string]any{"command": "docker push registry.example.test/api:1.4.0"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachRemote {
		t.Fatalf("remote push = %q", got)
	}
	args = map[string]any{"command": "docker push myuser/api:1.4.0"}
	if got := ResolveEffectReach("command", args, "/proj", nil); got != EffectReachRemote {
		t.Fatalf("docker hub = %q", got)
	}
}

func TestCargoPublishRuleRespectsEffectReach(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	var rule Rule
	for _, p := range cat.Packs {
		if p.ID != "publish-release" {
			continue
		}
		for _, r := range p.Rules {
			if r.Slug == "cargo-publish" {
				rule = r
			}
		}
	}
	if rule.Slug == "" {
		t.Fatal("cargo-publish rule missing")
	}
	remote := testEventWithReach("command", "cargo publish --registry local-registry", "/p", true, "proxy", "s", EffectReachRemote)
	if !rule.Matches(remote) {
		t.Fatal("remote reach should still match")
	}
	local := testEventWithReach("command", "cargo publish --registry local-registry", "/p", true, "proxy", "s", EffectReachLocal)
	if rule.Matches(local) {
		t.Fatal("local reach should not match")
	}
	unproven := testEventWithReach("command", "cargo publish --registry local-registry", "/p", true, "proxy", "s", EffectReachUnproven)
	if !rule.Matches(unproven) {
		t.Fatal("unproven reach should match (fail closed)")
	}
}
