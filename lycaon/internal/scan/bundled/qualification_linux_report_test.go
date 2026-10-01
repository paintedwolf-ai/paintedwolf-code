package bundled

import (
	"slices"
	"strings"
	"testing"
)

func linuxQualificationFixture() linuxPlatformReport {
	image := func(name string) linuxImage {
		return linuxImage{Path: name, SHA256: strings.Repeat("a", 64), Architecture: "arm64",
			GLIBCVersions: []string{"2.17", "2.34"}, Dependencies: []string{"libc.so.6"}}
	}
	standalone := []linuxImage{image("opengrep.bin"), image("semgrep/bin/opengrep-core")}
	return linuxPlatformReport{SchemaVersion: 1, Platform: "linux", Architecture: "arm64", GLIBCMax: "2.35",
		Outer: image("opengrep"), Standalone: standalone, Extracted: slices.Clone(standalone)}
}

func TestLinuxQualificationRequiresMatchingPackagedImages(t *testing.T) {
	fixture := linuxQualificationFixture()
	if err := validateLinuxPlatformReport(fixture, "arm64", strings.Repeat("a", 64)); err != nil {
		t.Fatalf("valid Linux qualification: %v", err)
	}
	changes := map[string]func(*linuxPlatformReport){
		"wrong architecture":        func(r *linuxPlatformReport) { r.Outer.Architecture = "amd64" },
		"wrong binary":              func(r *linuxPlatformReport) { r.Outer.SHA256 = strings.Repeat("b", 64) },
		"newer glibc":               func(r *linuxPlatformReport) { r.Outer.GLIBCVersions = []string{"2.36"} },
		"external launcher library": func(r *linuxPlatformReport) { r.Outer.Dependencies = []string{"libssl.so.3"} },
		"missing core":              func(r *linuxPlatformReport) { r.Standalone = r.Standalone[:1]; r.Extracted = r.Extracted[:1] },
		"different extracted bytes": func(r *linuxPlatformReport) { r.Extracted[0].SHA256 = strings.Repeat("b", 64) },
		"host runpath": func(r *linuxPlatformReport) {
			r.Standalone[0].RPaths = []string{"/build/python/lib"}
			r.Extracted = slices.Clone(r.Standalone)
		},
		"escaping runpath": func(r *linuxPlatformReport) {
			r.Standalone[0].RPaths = []string{"$ORIGIN/../../tmp"}
			r.Extracted = slices.Clone(r.Standalone)
		},
		"missing packaged library": func(r *linuxPlatformReport) {
			r.Standalone[0].Dependencies = []string{"libpython3.13.so.1.0"}
			r.Extracted = slices.Clone(r.Standalone)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			report := linuxQualificationFixture()
			change(&report)
			if err := validateLinuxPlatformReport(report, "arm64", strings.Repeat("a", 64)); err == nil {
				t.Fatal("invalid Linux qualification was accepted")
			}
		})
	}
}

func TestLinuxQualificationRequiresReachableLibraryPaths(t *testing.T) {
	for _, paths := range [][]string{nil, {"$ORIGIN/..", "$ORIGIN"}, {"$ORIGIN/../.."}} {
		report := linuxQualificationFixture()
		library := report.Standalone[0]
		library.Path = "libfixture.so"
		report.Standalone = append(report.Standalone, library)
		report.Standalone[1].Dependencies = []string{"libfixture.so"}
		report.Standalone[1].RPaths = paths
		report.Extracted = slices.Clone(report.Standalone)
		err := validateLinuxPlatformReport(report, "arm64", strings.Repeat("a", 64))
		wantValid := len(paths) == 1 && paths[0] == "$ORIGIN/../.."
		if (err == nil) != wantValid {
			t.Fatalf("library reachability for runpaths %v: valid=%v, error=%v", paths, wantValid, err)
		}
	}
}
