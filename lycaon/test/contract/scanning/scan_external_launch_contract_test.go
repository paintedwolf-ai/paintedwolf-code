package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScannerDriverLaunchKinds(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	externalSrc := filepath.Join(root, "lycaon", "internal", "scan", "drivers", "external", "scanner.go")
	bundledSrc := filepath.Join(root, "lycaon", "internal", "scan", "drivers", "bundled", "opengrep.go")
	probeSrc := filepath.Join(root, "lycaon", "internal", "scan", "catalog", "probe.go")

	externalBody, err := os.ReadFile(externalSrc)
	contractcheck.FailErr(t, "read external scanner driver", err)
	if strings.Contains(string(externalBody), "BundledScannerConfinement") {
		t.Fatal("external scanner driver applies BundledScannerConfinement")
	}
	if strings.Contains(string(externalBody), "AgentLaunch") {
		t.Fatal("external scanner driver uses AgentLaunch")
	}
	if !strings.Contains(string(externalBody), "ExternalScannerLaunch") {
		t.Fatal("external scanner driver missing ExternalScannerLaunch")
	}

	bundledBody, err := os.ReadFile(bundledSrc)
	contractcheck.FailErr(t, "read bundled OpenGrep driver", err)
	if !strings.Contains(string(bundledBody), "BundledScannerConfinement") {
		t.Fatal("bundled OpenGrep driver missing BundledScannerConfinement")
	}
	if !strings.Contains(string(bundledBody), "BundledScannerLaunch") {
		t.Fatal("bundled OpenGrep driver missing BundledScannerLaunch")
	}
	if strings.Contains(string(bundledBody), "AgentLaunch") {
		t.Fatal("bundled OpenGrep driver uses AgentLaunch")
	}

	probeBody, err := os.ReadFile(probeSrc)
	contractcheck.FailErr(t, "read scanner probe", err)
	if !strings.Contains(string(probeBody), "ExternalScannerLaunch") {
		t.Fatal("scanner probe missing ExternalScannerLaunch")
	}
	if strings.Contains(string(probeBody), "BundledScannerConfinement") {
		t.Fatal("scanner probe applies BundledScannerConfinement")
	}
}
