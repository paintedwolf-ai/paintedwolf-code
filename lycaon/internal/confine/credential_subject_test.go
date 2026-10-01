package confine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
)

func withCatalogue(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory; the catalogue is home-relative")
	}
	previous := credentialStorePathsSource.Load()
	SetCredentialStorePathsSource(func() []string { return credentialStorePathFixture })
	t.Cleanup(func() { credentialStorePathsSource.Store(previous) })
	return home
}

func TestClassifyBlockedWrite(t *testing.T) {
	home := withCatalogue(t)

	cases := []struct {
		name string
		path string
		want WriteSubjectKind
	}{
		{
			name: "key material classifies as key material",
			path: filepath.Join(home, ".ssh", "authorized_keys"),
			want: WriteSubjectKeyMaterial,
		},
		{
			name: "signing keys are key material even inside a CLI-auth directory",
			path: filepath.Join(home, ".docker", "trust", "private", "root.key"),
			want: WriteSubjectKeyMaterial,
		},
		{
			name: "a catalogued store offers the file",
			path: filepath.Join(home, ".docker", "config.json"),
			want: WriteSubjectCredentialStore,
		},
		{
			name: "a file inside a catalogued directory offers that file",
			path: filepath.Join(home, ".aws", "credentials"),
			want: WriteSubjectCredentialStore,
		},
		{
			name: "an ordinary path stays ordinary",
			path: filepath.Join(home, "code", "app", "main.go"),
			want: WriteSubjectOrdinary,
		},
		{
			name: "an ancestor of a store is ordinary",
			path: home,
			want: WriteSubjectOrdinary,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyBlockedWrite(tc.path)
			if got.Kind != tc.want {
				t.Fatalf("kind = %q, want %q", got.Kind, tc.want)
			}
			if tc.want == WriteSubjectCredentialStore && got.GrantPath != fspath.CanonicalPath(tc.path) {
				t.Errorf("grant path = %q, want the blocked file itself", got.GrantPath)
			}
		})
	}
}

func TestClassificationWithoutACatalogueFallsBackToTheBoundary(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	previous := credentialStorePathsSource.Load()
	SetCredentialStorePathsSource(nil)
	t.Cleanup(func() { credentialStorePathsSource.Store(previous) })

	if got := ClassifyBlockedWrite(filepath.Join(home, ".docker", "config.json")); got.Kind != WriteSubjectOrdinary {
		t.Fatalf("kind = %q, want the ordinary ladder when nothing is catalogued", got.Kind)
	}
	if got := ClassifyBlockedWrite(filepath.Join(home, ".ssh", "id_ed25519")); got.Kind != WriteSubjectKeyMaterial {
		t.Fatalf("kind = %q, want key material without any catalogue", got.Kind)
	}
}

func TestTemporaryRewriteSiblingsResolveToTheirTarget(t *testing.T) {
	home := withCatalogue(t)
	docker := filepath.Join(home, ".docker")
	target := fspath.CanonicalPath(filepath.Join(docker, "config.json"))

	for _, refused := range []string{
		filepath.Join(docker, ".tmp-config.json2451927"),
		filepath.Join(docker, "config.json1234567"),
		filepath.Join(docker, "config.json.lock"),
	} {
		got := ClassifyBlockedWrite(refused)
		if got.Kind != WriteSubjectCredentialStore {
			t.Errorf("%q classified %q; a rewrite sibling has to reach the same card", refused, got.Kind)
			continue
		}
		if got.GrantPath != target {
			t.Errorf("%q resolved to %q, want the credential file %q", refused, got.GrantPath, target)
		}
	}
}

func TestUnrelatedNeighboursDoNotResolveToACredentialTarget(t *testing.T) {
	home := withCatalogue(t)
	docker := filepath.Join(home, ".docker")

	got := ClassifyBlockedWrite(filepath.Join(docker, "cli-plugins", "docker-buildx"))
	if got.Kind == WriteSubjectCredentialStore {
		t.Fatalf("a plugin binary resolved to a protected grant (%q)", got.GrantPath)
	}
}

func TestRewriteSiblingsResolveInsideDirectoryCatalogedStores(t *testing.T) {
	home := withCatalogue(t)
	target := fspath.CanonicalPath(filepath.Join(home, ".aws", "credentials"))

	got := ClassifyBlockedWrite(filepath.Join(home, ".aws", ".tmp-credentials88123"))
	if got.GrantPath != target {
		t.Fatalf("resolved to %q, want the credential file %q", got.GrantPath, target)
	}
}
