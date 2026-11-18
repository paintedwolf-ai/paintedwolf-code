package contract

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type passwordVendorPin struct {
	ID         string   `yaml:"id"`
	Upstream   string   `yaml:"upstream"`
	Ref        string   `yaml:"ref"`
	Commit     string   `yaml:"commit"`
	TreeSHA256 string   `yaml:"tree_sha256"`
	License    string   `yaml:"license"`
	Include    []string `yaml:"include"`
}

type provenanceFile struct {
	Vendors []passwordVendorPin `yaml:"vendors"`
}

func loadPasswordProvenance() ([]passwordVendorPin, error) {
	raw, err := config.Read(config.SecretMintProvenance)
	if err != nil {
		return nil, fmt.Errorf("zxcvbn provenance: %w", err)
	}
	var file provenanceFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return nil, fmt.Errorf("zxcvbn provenance: %w", err)
	}
	return file.Vendors, nil
}

var (
	zxcvbnCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	zxcvbnTreeSHA   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func TestZxcvbnPasswordsCatalogIsPinned(t *testing.T) {
	t.Parallel()
	vendors, err := loadPasswordProvenance()
	contractcheck.FailErr(t, "load password provenance", err)
	if len(vendors) == 0 {
		t.Fatal("zxcvbn-provenance.yaml has no vendors")
	}
	for _, vendor := range vendors {
		if !zxcvbnCommitSHA.MatchString(vendor.Commit) {
			t.Errorf("%s: commit %q is not a 40-hex SHA — vendoring must pin an immutable object", vendor.ID, vendor.Commit)
		}
		if !zxcvbnTreeSHA.MatchString(vendor.TreeSHA256) {
			t.Errorf("%s: tree_sha256 %q is not a 64-hex digest — run scripts/vendor-zxcvbn-passwords.sh", vendor.ID, vendor.TreeSHA256)
		}
		if !strings.HasPrefix(vendor.Upstream, "https://") {
			t.Errorf("%s: upstream %q is not https", vendor.ID, vendor.Upstream)
		}
		if len(vendor.Include) == 0 {
			t.Errorf("%s: no include: paths", vendor.ID)
		}
	}
}

func TestZxcvbnPasswordsVendorTreeHoldsLicenseAndList(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "host", "secret-mint", "vendor", "zxcvbn-passwords")
	for _, name := range []string{"LICENSE", "passwords.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing from vendored tree", name)
		}
	}
}

func TestZxcvbnPasswordsVendorCheckMatchesLedger(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	cmd := exec.Command(filepath.Join(root, "scripts", "vendor-zxcvbn-passwords.sh"), "--check")
	cmd.Dir = root
	cmd.Env = lyexec.InheritedEnviron()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vendor-zxcvbn-passwords.sh --check failed: %v\n%s", err, out)
	}
}
