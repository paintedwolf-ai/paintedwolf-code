package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestReleaseBlastRadiusNoCredentialsInCorpus(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	corpus := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus")
	credNames := map[string]struct{}{}
	for _, rel := range localdata.CredentialRelPaths() {
		credNames[filepath.Base(rel)] = struct{}{}
	}
	err := filepath.Walk(corpus, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := info.Name()
		if base == "store.db-wal" || base == "store.db-shm" || base == "store.db-journal" {
			t.Errorf("forbidden sqlite sidecar in corpus: %s", path)
		}
		if _, ok := credNames[base]; ok {
			relative, err := filepath.Rel(corpus, path)
			if err != nil {
				return err
			}
			projectMCP, err := filepath.Match(filepath.Join("*", "project-root", testutil.OverlayFixtureDirName, "mcp.yaml"), relative)
			if err != nil {
				return err
			}
			if projectMCP {
				// Only an empty provider list belongs in this fixture.
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.TrimSpace(string(data)) != "providers: []" {
					t.Errorf("project MCP fixture must declare an empty provider list: %s", path)
				}
			} else {
				t.Errorf("credential filename in corpus: %s", path)
			}
		}
		if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
			t.Errorf("key-like file in corpus: %s", path)
		}
		if info.Size() > 0 && info.Size() < 1<<20 {
			raw, rerr := os.ReadFile(path)
			if rerr == nil && bytesContainPrivateKeyPEM(raw) {
				t.Errorf("private key PEM in corpus: %s", path)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk corpus", err)
}

func bytesContainPrivateKeyPEM(raw []byte) bool {
	s := string(raw)
	return strings.Contains(s, "BEGIN RSA PRIVATE KEY") ||
		strings.Contains(s, "BEGIN OPENSSH PRIVATE KEY") ||
		strings.Contains(s, "BEGIN PRIVATE KEY")
}
