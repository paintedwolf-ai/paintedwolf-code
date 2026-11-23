package confine

import (
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func cacheRootFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("home", home)
	for name, relative := range map[string]string{
		"XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state",
		"XDG_CONFIG_HOME": "config", "APPDATA": "config", "LYCAON_SANDBOX_WRITE_ROOTS": "granted",
	} {
		t.Setenv(name, filepath.Join(home, relative))
	}
	return home
}

func assertCacheRootSet(t *testing.T, home, config string, conventions []string) {
	t.Helper()
	want := append(slices.Clone(conventions),
		filepath.Join(home, ".bun/install/cache"), filepath.Join(home, ".npm/_cacache"),
		filepath.Join(home, ".cargo/registry"), filepath.Join(home, ".cargo/git"),
		filepath.Join(config, "go/telemetry"), fspath.CanonicalPath(filepath.Join(home, "granted")))
	got := standardCacheDataRoots()
	if !slices.Equal(got, want) {
		t.Errorf("default root set:\n got %q\nwant %q", got, want)
	}
}

// The fixture exercises cache authority outside the machine temp floor.
func privateCacheFixture(t *testing.T) string {
	t.Helper()
	account, err := user.Current()
	testutil.FailErr(t, "locate account home", err)
	dir, err := os.MkdirTemp(account.HomeDir, ".pw-cache-test-") //nolint:usetesting // Cache must lie outside shared temp.
	testutil.FailErr(t, "create cache fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove cache fixture", os.RemoveAll(dir)) })
	return dir
}

func TestSharedTempCacheOverlap(t *testing.T) {
	cache := privateCacheFixture(t)
	temp := filepath.Join(cache, "temporary")
	nestedCache := filepath.Join(temp, "nested-cache")
	attached := filepath.Join(temp, "attached")
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", nestedCache)
	t.Setenv("TMPDIR", temp)
	rules, err := filesystemRules(Confinement{Roots: []string{attached}})
	testutil.FailErr(t, "compile cache rules", err)
	for _, path := range []string{filepath.Join(temp, "out"), filepath.Join(nestedCache, ".cargo", "registry", "index")} {
		for _, access := range []FilesystemAccess{AccessRead, AccessWrite} {
			if got := rules.Verdict(access, path); got.Layer != FloorSharedTemp {
				t.Fatalf("cache overlap %s at %s: %+v", access, path, got)
			}
		}
	}
	for _, path := range []string{filepath.Join(cache, "index"), filepath.Join(attached, "out")} {
		for _, access := range []FilesystemAccess{AccessRead, AccessWrite} {
			if got := rules.Verdict(access, path); !got.Allowed {
				t.Errorf("%s refused at %s: %+v", access, path, got)
			}
		}
	}
}

func TestSharedTempPolicyLayersNestedRootsAndWriteOnlyLeases(t *testing.T) {
	parent := privateCacheFixture(t)
	temp := filepath.Join(parent, "temp")
	attached := filepath.Join(temp, "attached")
	scratch := filepath.Join(temp, "scratch-own")
	stateDir := filepath.Join(temp, "state")
	siblingScratch := filepath.Join(stateDir, "scratch", "other-session")
	home := filepath.Join(temp, "home")
	sshKey := filepath.Join(home, ".ssh", "id_ed25519")
	excluded := filepath.Join(temp, "excluded")
	writeOnlyDir := filepath.Join(temp, "export")
	readDir := filepath.Join(temp, "exchange")
	exactInput := filepath.Join(temp, "incoming.csv")
	siblingInput := filepath.Join(temp, "other.csv")

	for _, dir := range []string{attached, scratch, siblingScratch, filepath.Dir(sshKey), excluded, writeOnlyDir, readDir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	for _, file := range []string{exactInput, siblingInput, sshKey, filepath.Join(stateDir, "api.token")} {
		testutil.FailErr(t, "write "+file, os.WriteFile(file, []byte("x"), 0o600))
	}

	t.Setenv("TMPDIR", temp)
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", stateDir)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")

	// An incidental parent root above temp conveys no shared-temp authority.
	incidentalState := filepath.Join(privateCacheFixture(t), "state")
	testutil.FailErr(t, "mkdir incidental state", os.MkdirAll(incidentalState, 0o700))
	t.Setenv("LYCAON_CONFIG_DIR", incidentalState)
	parentRules, err := filesystemRules(Confinement{Roots: []string{parent}})
	testutil.FailErr(t, "compile incidental parent rules", err)
	for _, access := range []FilesystemAccess{AccessRead, AccessWrite} {
		if got := parentRules.Verdict(access, siblingInput); got != (FloorVerdict{Layer: FloorSharedTemp}) {
			t.Fatalf("incidental parent %s %s = %+v, want shared-temp floor", access, siblingInput, got)
		}
	}

	// Attached roots inside temp, session scratch, exact read grants, and
	// write-only leases keep independent directional boundaries beneath stronger floors.
	t.Setenv("LYCAON_CONFIG_DIR", stateDir)
	c := Confinement{
		Roots:               []string{attached},
		GrantedWriteRoots:   []string{writeOnlyDir},
		Workspaces:          WorkspaceRoots{SessionScratch: scratch},
		ReadDenyPaths:       []string{excluded},
		ProtectedReadGrants: []ProtectedPathGrant{NewProtectedPathGrant(exactInput), NewProtectedPathGrant(readDir)},
	}
	rules, err := filesystemRules(c)
	testutil.FailErr(t, "compile shared-temp rules", err)

	allowed := FloorVerdict{Allowed: true}
	for _, tc := range []struct {
		name   string
		access FilesystemAccess
		path   string
		want   FloorVerdict
	}{
		{"ungranted temp file read", AccessRead, siblingInput, FloorVerdict{Layer: FloorSharedTemp}},
		{"ungranted temp dir list", AccessRead, temp, FloorVerdict{Layer: FloorSharedTemp}},
		{"ungranted temp write", AccessWrite, siblingInput, FloorVerdict{Layer: FloorSharedTemp}},
		{"exact input read", AccessRead, exactInput, allowed},
		{"exact input stays read-only", AccessWrite, exactInput, FloorVerdict{Layer: FloorSharedTemp}},
		{"granted exchange dir read", AccessRead, filepath.Join(readDir, "a.json"), allowed},
		{"granted exchange dir stays read-only", AccessWrite, filepath.Join(readDir, "a.json"), FloorVerdict{Layer: FloorSharedTemp}},
		{"write-only dir write", AccessWrite, filepath.Join(writeOnlyDir, "out.csv"), allowed},
		{"write-only dir stays unreadable", AccessRead, filepath.Join(writeOnlyDir, "out.csv"), FloorVerdict{Layer: FloorSharedTemp}},
		{"write-only dir stays unlistable", AccessRead, writeOnlyDir, FloorVerdict{Layer: FloorSharedTemp}},
		{"attached temp project read", AccessRead, filepath.Join(attached, "main.go"), allowed},
		{"attached temp project write", AccessWrite, filepath.Join(attached, "main.go"), allowed},
		{"attached temp project AGENTS.md", AccessWrite, filepath.Join(attached, "AGENTS.md"), FloorVerdict{Layer: FloorAgentPolicy}},
		{"own scratch read", AccessRead, filepath.Join(scratch, "work.txt"), allowed},
		{"own scratch write", AccessWrite, filepath.Join(scratch, "work.txt"), allowed},
		{"sibling scratch read stays control-plane", AccessRead, filepath.Join(siblingScratch, "secret.txt"), FloorVerdict{Layer: FloorReadControlPlane}},
		{"sibling scratch write stays control-plane", AccessWrite, filepath.Join(siblingScratch, "secret.txt"), FloorVerdict{Layer: FloorControlPlane}},
		{"key material in temp stays protected on read", AccessRead, sshKey, FloorVerdict{Layer: FloorReadKeyMaterial}},
		{"key material in temp stays protected on write", AccessWrite, sshKey, FloorVerdict{Layer: FloorProtected}},
		{"configured read exclusion in temp", AccessRead, filepath.Join(excluded, "src.go"), FloorVerdict{Layer: FloorReadConfigured}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Verdict(tc.access, tc.path); got != tc.want {
				t.Fatalf("%s %s = %+v, want %+v", tc.access, tc.path, got, tc.want)
			}
		})
	}

	// Even a full-temp directory read grant cannot open nested stronger floors.
	wide := c
	wide.ProtectedReadGrants = []ProtectedPathGrant{NewProtectedPathGrant(temp)}
	wideRules, err := filesystemRules(wide)
	testutil.FailErr(t, "compile wide temp read rules", err)
	if got := wideRules.Verdict(AccessRead, siblingInput); !got.Allowed {
		t.Fatalf("full temp read grant refused ordinary temp file: %+v", got)
	}
	for _, tc := range []struct {
		path  string
		layer FloorLayer
	}{
		{filepath.Join(siblingScratch, "secret.txt"), FloorReadControlPlane},
		{filepath.Join(stateDir, "api.token"), FloorReadControlPlane},
		{sshKey, FloorReadKeyMaterial},
		{filepath.Join(excluded, "src.go"), FloorReadConfigured},
	} {
		if got := wideRules.Verdict(AccessRead, tc.path); got != (FloorVerdict{Layer: tc.layer}) {
			t.Fatalf("full temp read grant lifted %s on %s: %+v", tc.layer, tc.path, got)
		}
	}

	// Durable write-only leases remain write-only at native entry points.
	SetGrantedWriteRootsSource(func(projectID string) []string {
		if projectID == "proj-old-lease" {
			return []string{temp}
		}
		return nil
	})
	t.Cleanup(func() { SetGrantedWriteRootsSource(nil) })
	writeRoot, err := NativeFilesystemRootForPath("proj-old-lease", nil, nil, "", filepath.Join(temp, "out.txt"), true)
	testutil.FailErr(t, "native write root from durable lease", err)
	if writeRoot != fspath.CanonicalPath(temp) {
		t.Fatalf("durable write lease did not cover write: got %q, want %q", writeRoot, fspath.CanonicalPath(temp))
	}
	readRoot, err := NativeReadRootForPath("proj-old-lease", nil, nil, nil, []string{temp}, "", "", siblingInput)
	testutil.FailErr(t, "native read root with write-only lease", err)
	if readRoot != "" {
		t.Fatalf("write-only lease opened native shared-temp read: got %q", readRoot)
	}
}
