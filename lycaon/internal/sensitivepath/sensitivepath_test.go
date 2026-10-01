package sensitivepath_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func bundled(t *testing.T) *sensitivepath.Catalog {
	t.Helper()
	cat, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load bundled catalog", err)
	return cat
}

func home(t *testing.T) string {
	t.Helper()
	dir, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	return dir
}

// Catalog modes apply only to their declared direction.
func TestModeSeparatesTheDirections(t *testing.T) {
	cat, h := bundled(t), home(t)
	cases := []struct {
		path     string
		fires    sensitivepath.Mode
		notFires sensitivepath.Mode
		wantID   string
	}{
		{filepath.Join(h, "Documents", "tax.pdf"), sensitivepath.ModeRead, sensitivepath.ModeWrite, "user-documents"},
		{filepath.Join(h, "Library", "LaunchAgents", "x.plist"), sensitivepath.ModeWrite, sensitivepath.ModeRead, "launch-services"},
		{filepath.Join(h, ".zshrc"), sensitivepath.ModeWrite, sensitivepath.ModeRead, "shell-startup"},
		{filepath.Join(h, ".gitconfig"), sensitivepath.ModeWrite, sensitivepath.ModeRead, "git-config"},
	}
	for _, tc := range cases {
		t.Run(tc.wantID, func(t *testing.T) {
			m, ok := cat.Match(tc.path, tc.fires)
			if !ok || m.ID != tc.wantID {
				t.Fatalf("%s in %s = %+v/%v, want %s", tc.path, tc.fires, m, ok, tc.wantID)
			}
			if _, ok := cat.Match(tc.path, tc.notFires); ok {
				t.Fatalf("%s must be inert for %s", tc.path, tc.notFires)
			}
		})
	}
}

// Key material is sensitive in both directions.
func TestKeyMaterialFiresBothWays(t *testing.T) {
	cat, h := bundled(t), home(t)
	path := filepath.Join(h, ".ssh", "id_ed25519")
	for _, mode := range []sensitivepath.Mode{sensitivepath.ModeRead, sensitivepath.ModeWrite} {
		if m, ok := cat.Match(path, mode); !ok || m.ID != "ssh-keys" {
			t.Fatalf("%s = %+v/%v, want ssh-keys", mode, m, ok)
		}
	}
}

func TestClassifyResolvedFollowsSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-sensitive")
	testutil.FailErr(t, "mkdir real", os.MkdirAll(real, 0o755))
	// Canonicalize the catalog path before creating its alias.
	resolvedReal, err := filepath.EvalSymlinks(real)
	testutil.FailErr(t, "eval symlinks real", err)

	catalogDir := t.TempDir()
	yamlBody := "version: 1\nlocations:\n  - id: test-sensitive\n    title: Test Sensitive\n    mode: write\n    protected: true\n    paths: [\"" + filepath.ToSlash(resolvedReal) + "\"]\n"
	testutil.FailErr(t, "write catalog", os.WriteFile(filepath.Join(catalogDir, "test.yaml"), []byte(yamlBody), 0o644))
	cat, err := sensitivepath.Load(sensitivepath.Dir(catalogDir))
	testutil.FailErr(t, "load catalog", err)

	alias := filepath.Join(dir, "alias")
	testutil.FailErr(t, "symlink", os.Symlink(real, alias))
	target := filepath.Join(alias, "secret.txt")

	direct, okDirect := cat.Match(target, sensitivepath.ModeWrite)
	if !okDirect || direct.ID != "test-sensitive" {
		t.Fatalf("Match(%s) = %+v/%v, want test-sensitive", target, direct, okDirect)
	}
	m, ok := cat.ClassifyResolved(target, sensitivepath.ModeWrite)
	if !ok || m.ID != "test-sensitive" || !m.Protected {
		t.Fatalf("ClassifyResolved(%s) = %+v/%v, want protected test-sensitive", target, m, ok)
	}
}

// Longest prefix wins.
func TestLongestPrefixWins(t *testing.T) {
	cat, h := bundled(t), home(t)
	deep := filepath.Join(h, ".aws", "sso", "cache", "token.json")
	if m, ok := cat.Match(deep, sensitivepath.ModeRead); !ok || m.ID != "token-cache" {
		t.Fatalf("deep read = %+v/%v, want token-cache over cloud-credentials", m, ok)
	}
	shallow := filepath.Join(h, ".aws", "config")
	if m, ok := cat.Match(shallow, sensitivepath.ModeRead); !ok || m.ID != "cloud-credentials" {
		t.Fatalf("shallow read = %+v/%v, want cloud-credentials", m, ok)
	}
}

// Basename globs match sensitive files outside cataloged directories.
func TestBasenameGlobsMatchAnywhere(t *testing.T) {
	cat := bundled(t)
	cases := []struct{ path, wantID string }{
		{"/opt/someones-stuff/id_rsa", "private-key-file"},
		{"/srv/deploy/server.pem", "private-key-file"},
		{"/var/tmp/checkout/.env", "loose-credential-file"},
		{"/var/tmp/checkout/.env.production", "loose-credential-file"},
		{"/data/service-account-prod.json", "loose-credential-file"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			m, ok := cat.Match(tc.path, sensitivepath.ModeRead)
			if !ok || m.ID != tc.wantID {
				t.Fatalf("= %+v/%v, want %s", m, ok, tc.wantID)
			}
		})
	}
}

// Path matches outrank name matches.
func TestPathMatchOutranksNameMatch(t *testing.T) {
	cat, h := bundled(t), home(t)
	m, ok := cat.Match(filepath.Join(h, "Documents", ".env"), sensitivepath.ModeRead)
	if !ok || m.ID != "user-documents" {
		t.Fatalf("= %+v/%v, want user-documents", m, ok)
	}
}

// Protected entries remain sensitive inside attached roots.
func TestProtectedFlagSeparatesCredentialsFromPrivacyAreas(t *testing.T) {
	cat, h := bundled(t), home(t)
	protected := []struct {
		path string
		mode sensitivepath.Mode
	}{
		{filepath.Join(h, "repo", ".env"), sensitivepath.ModeRead},
		{filepath.Join(h, ".ssh", "id_ed25519"), sensitivepath.ModeRead},
		{filepath.Join(h, ".aws", "credentials"), sensitivepath.ModeWrite},
		{filepath.Join(h, ".netrc"), sensitivepath.ModeRead},
	}
	for _, tc := range protected {
		m, ok := cat.Match(tc.path, tc.mode)
		if !ok || !m.Protected {
			t.Errorf("%s (%s) = %+v/%v, want a protected match", tc.path, tc.mode, m, ok)
		}
	}
	m, ok := cat.Match(filepath.Join(h, "Documents", "tax.pdf"), sensitivepath.ModeRead)
	if !ok || m.Protected {
		t.Errorf("Documents = %+v/%v, want a match that attached roots may suppress", m, ok)
	}
}

func TestOrdinaryDevelopmentPathsAreQuiet(t *testing.T) {
	cat, h := bundled(t), home(t)
	for _, path := range []string{
		filepath.Join(h, ".cargo", "registry", "cache", "x.crate"),
		filepath.Join(h, ".npm", "_cacache", "index-v5"),
		filepath.Join(h, "go", "pkg", "mod", "example.com", "x.zip"),
		filepath.Join(h, "Library", "Caches", "go-build", "aa"),
		filepath.Join(h, ".gradle", "caches", "modules-2"),
		"/tmp/build-output",
		"/usr/local/lib/node_modules/x",
	} {
		for _, mode := range []sensitivepath.Mode{sensitivepath.ModeRead, sensitivepath.ModeWrite} {
			if m, ok := cat.Match(path, mode); ok {
				t.Errorf("%s (%s) matched %s — ordinary work must stay quiet", path, mode, m.ID)
			}
		}
	}
}

// Overlay entries replace bundled entries by id.
func TestOverlayReplacesByID(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(`
version: 1
locations:
  - id: user-documents
    title: Only my private notes
    mode: read
    paths: ["~/Documents/private"]
`), 0o600))

	cat, err := sensitivepath.Load(sensitivepath.Bundled(), sensitivepath.Dir(dir))
	testutil.FailErr(t, "load with overlay", err)

	h := home(t)
	if m, ok := cat.Match(filepath.Join(h, "Documents", "private", "x.md"), sensitivepath.ModeRead); !ok ||
		m.Title != "Only my private notes" {
		t.Fatalf("overlay entry = %+v/%v", m, ok)
	}
	if m, ok := cat.Match(filepath.Join(h, "Documents", "tax.pdf"), sensitivepath.ModeRead); ok {
		t.Fatalf("the bundled entry should have been replaced, still matched %s", m.ID)
	}
}

// Missing overlay directories are empty layers.
func TestMissingOverlayIsNotAnError(t *testing.T) {
	_, err := sensitivepath.Load(sensitivepath.Bundled(), sensitivepath.Dir(filepath.Join(t.TempDir(), "absent")))
	testutil.FailErr(t, "load with absent overlay", err)
}

// Malformed entries fail the whole catalog load.
func TestMalformedEntryFailsTheLoad(t *testing.T) {
	for name, body := range map[string]string{
		"no id":      "version: 1\nlocations:\n  - title: x\n    mode: read\n    paths: [\"/tmp\"]\n",
		"bad mode":   "version: 1\nlocations:\n  - id: x\n    title: x\n    mode: sideways\n    paths: [\"/tmp\"]\n",
		"no subject": "version: 1\nlocations:\n  - id: x\n    title: x\n    mode: read\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o600))
			if _, err := sensitivepath.Load(sensitivepath.Dir(dir)); err == nil {
				t.Fatal("malformed catalog loaded without error")
			}
		})
	}
}

// Bundled entries require unique ids and visible titles.
func TestBundledCatalogIsWellFormed(t *testing.T) {
	cat := bundled(t)
	locs := cat.Locations()
	if len(locs) < 20 {
		t.Fatalf("bundled catalog has %d entries, want the full set", len(locs))
	}
	seen := map[string]struct{}{}
	for _, loc := range locs {
		if loc.Title == "" {
			t.Errorf("%s has no title, so its card would show no reason", loc.ID)
		}
		if _, dup := seen[loc.ID]; dup {
			t.Errorf("%s appears twice", loc.ID)
		}
		seen[loc.ID] = struct{}{}
	}
}

// TestMatchCoveringSeesEntriesUnderAProposedRoot checks ancestor proposals.
func TestMatchCoveringSeesEntriesUnderAProposedRoot(t *testing.T) {
	cat := bundled(t)
	h := home(t)

	root := filepath.Join(h, ".cargo")
	if _, ok := cat.Match(root, sensitivepath.ModeWrite); ok {
		t.Fatalf("%s is not itself a catalog entry; this test would prove nothing", root)
	}
	m, ok := cat.MatchCovering(root, sensitivepath.ModeWrite)
	if !ok {
		t.Fatalf("a grant on %s confers the credential entry beneath it and must be seen", root)
	}
	if m.Title == "" {
		t.Error("a covering match must cite a title, or its card renders no reason")
	}

	// Unrelated ancestors do not match.
	if _, ok := cat.MatchCovering("/opt/some-unlisted-toolchain", sensitivepath.ModeWrite); ok {
		t.Error("an unlisted root must not become sensitive by accident")
	}

	// Descendants are handled by Match.
	if _, ok := cat.MatchCovering(filepath.Join(h, ".cargo", "credentials.toml"), sensitivepath.ModeWrite); ok {
		t.Error("MatchCovering must not re-report what Match already covers")
	}
}
