package contract

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// These tests keep platform replicas aligned with platformfloor.

var homebrewMacOSSymbols = map[int]string{
	13: "ventura",
	14: "sonoma",
	15: "sequoia",
	26: "tahoe",
}

func TestPlatformFloorTauriConfMatchesSSOT(t *testing.T) {
	t.Parallel()
	raw := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon-den/src-tauri/tauri.conf.json")

	var conf struct {
		Bundle struct {
			MacOS struct {
				MinimumSystemVersion string `json:"minimumSystemVersion"`
			} `json:"macOS"`
		} `json:"bundle"`
	}
	if err := json.Unmarshal([]byte(raw), &conf); err != nil {
		testutil.FailErr(t, "parse tauri.conf.json", err)
	}

	want := platformfloor.MacOSMin()
	got := conf.Bundle.MacOS.MinimumSystemVersion
	if got != want {
		t.Fatalf("tauri.conf.json bundle.macOS.minimumSystemVersion = %q, want %q (the floor SSOT in lycaon/internal/platformfloor/macos_floor.txt).\n"+
			"A minimumSystemVersion below the real floor lets LaunchServices start an app whose sidecar cannot run.", got, want)
	}
}

func TestPlatformFloorCaskMatchesSSOT(t *testing.T) {
	t.Parallel()
	major, _, err := platformfloor.MacOSMinParts()
	if err != nil {
		testutil.FailErr(t, "parse macOS floor", err)
	}

	symbol, ok := homebrewMacOSSymbols[major]
	if !ok {
		t.Fatalf("no Homebrew cask symbol known for macOS major %d.\n"+
			"Add a row to homebrewMacOSSymbols in this test, then update packaging/homebrew/painted-wolf-code.rb.tmpl to match.", major)
	}

	tmpl := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "packaging/homebrew/painted-wolf-code.rb.tmpl")
	want := fmt.Sprintf("depends_on macos: %q", ">= :"+symbol)
	if !strings.Contains(tmpl, want) {
		t.Fatalf("cask template is missing %s.\n"+
			"The cask is the third replica of the floor (macos_floor.txt says %s) and is the install path that skips Finder entirely.",
			want, platformfloor.MacOSMin())
	}
}

func TestPlatformFloorAboveGoToolchain(t *testing.T) {
	t.Parallel()
	major, _, err := platformfloor.MacOSMinParts()
	if err != nil {
		testutil.FailErr(t, "parse macOS floor", err)
	}
	if major < platformfloor.GoToolchainMacOSMinMajor {
		t.Fatalf("macOS floor major %d is below the Go toolchain floor %d — the product floor can never be lower than what the toolchain itself requires",
			major, platformfloor.GoToolchainMacOSMinMajor)
	}
}

func TestBundleScriptPinsDeploymentTarget(t *testing.T) {
	t.Parallel()
	// The shared staging script pins the sidecar build.
	stageScript := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/stage-engine.sh")
	for _, want := range []string{
		"export MACOSX_DEPLOYMENT_TARGET=",
		"export CGO_ENABLED=1",
		"BUILD_ARGS=(-trimpath",
	} {
		if !strings.Contains(stageScript, want) {
			t.Fatalf("scripts/stage-engine.sh is missing %q.\n"+
				"Without the pin, the shipped floor is whatever SDK the build host happened to have; without -trimpath the binary leaks build-machine paths and 4.8's BUILD_PATH_LEAK check cannot pass.", want)
		}
	}

	bundleScript := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/den-build-bundle.sh")
	if !strings.Contains(bundleScript, "export MACOSX_DEPLOYMENT_TARGET=") {
		t.Fatal("scripts/den-build-bundle.sh is missing \"export MACOSX_DEPLOYMENT_TARGET=\" — the Rust/Tauri build needs the same floor pin as the sidecar")
	}
}

func releaseMatrixArches(t *testing.T) []string {
	t.Helper()
	var catalog releasePlatformCatalog
	err := json.Unmarshal(
		[]byte(contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "packaging/release-platforms.json")),
		&catalog,
	)
	testutil.FailErr(t, "parse release platform catalog", err)
	var got []string
	for _, platform := range catalog.Platforms {
		if platform.GOOS == "darwin" {
			got = append(got, platform.ArtifactArch)
		}
	}
	sort.Strings(got)
	return got
}

func TestReleaseMatrixMatchesDarwinArches(t *testing.T) {
	t.Parallel()
	want := make([]string, 0, len(platformfloor.DarwinArches()))
	for _, goArch := range platformfloor.DarwinArches() {
		machO, ok := platformfloor.MachOArchForGoArch(goArch)
		if !ok {
			t.Fatalf("DarwinArches entry %q has no MachOArchForGoArch mapping", goArch)
		}
		want = append(want, machO)
	}
	sort.Strings(want)

	got := releaseMatrixArches(t)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("release.yml matrix arches = %v, want %v (derived from platformfloor.DarwinArches).\n"+
			"The arch set is one SSOT: adding or dropping a slice is one edit in platformfloor, and this test names every consumer that must follow.", got, want)
	}
}

// The Linux floor is one SSOT: the package dependency catalog renders from it,
// and the release catalog's Linux rows follow the shipped architectures.
func TestLinuxFloorFeedsPackagingAndCatalog(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for name, value := range map[string]string{
		"kernel":    platformfloor.LinuxKernelMin(),
		"glibc":     platformfloor.LinuxGlibcMin(),
		"webkitgtk": platformfloor.LinuxWebKitGTKMin(),
	} {
		if value == "" {
			t.Fatalf("Linux %s floor is empty: lycaon/internal/platformfloor/linux_floor.txt must declare it", name)
		}
	}
	dependencies := contractcheck.ReadRepoFile(t, root, "packaging/linux/dependencies.json")
	var catalog struct {
		FormatVersion int `json:"format_version"`
		Deb           struct {
			Depends []string `json:"depends"`
		} `json:"deb"`
		Rpm struct {
			Depends []string `json:"depends"`
		} `json:"rpm"`
	}
	testutil.FailErr(t, "parse packaging/linux/dependencies.json", json.Unmarshal([]byte(dependencies), &catalog))
	if catalog.FormatVersion != 1 {
		t.Fatalf("dependencies.json format_version = %d", catalog.FormatVersion)
	}
	for family, depends := range map[string][]string{"deb": catalog.Deb.Depends, "rpm": catalog.Rpm.Depends} {
		joined := strings.Join(depends, "\n")
		for _, placeholder := range []string{"{glibc}", "{webkitgtk}"} {
			if !strings.Contains(joined, placeholder) {
				t.Fatalf("%s dependencies never render the %s floor; constraints must come from linux_floor.txt, not literals", family, placeholder)
			}
		}
	}
	config := contractcheck.ReadRepoFile(t, root, "scripts/linux-package-config.py")
	if !strings.Contains(config, "platformfloor\" / \"linux_floor.txt\"") || !strings.Contains(config, "dependencies.json") {
		t.Fatal("scripts/linux-package-config.py must render package dependencies from the floor file and the dependency catalog")
	}
	var platforms struct {
		Platforms []struct {
			GOOS         string   `json:"goos"`
			GOARCH       string   `json:"goarch"`
			ArtifactArch string   `json:"artifact_arch"`
			PackageExts  []string `json:"package_extensions"`
		} `json:"platforms"`
	}
	testutil.FailErr(t, "parse release platform catalog", json.Unmarshal(
		[]byte(contractcheck.ReadRepoFile(t, root, "packaging/release-platforms.json")), &platforms))
	var got []string
	for _, platform := range platforms.Platforms {
		if platform.GOOS != "linux" {
			continue
		}
		elf, ok := platformfloor.ELFArchForGoArch(platform.GOARCH)
		if !ok || elf != platform.ArtifactArch {
			t.Fatalf("Linux row %s/%s is not a shipped architecture (%v, %q)", platform.GOOS, platform.GOARCH, ok, elf)
		}
		if strings.Join(platform.PackageExts, ",") != "deb,rpm" {
			t.Fatalf("Linux row %s ships %v, want both package families", platform.ArtifactArch, platform.PackageExts)
		}
		got = append(got, platform.ArtifactArch)
	}
	sort.Strings(got)
	want := platformfloor.LinuxELFArches()
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("release catalog Linux arches = %v, want %v (derived from platformfloor.LinuxArches)", got, want)
	}
}
