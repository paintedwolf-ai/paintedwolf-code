package bundled_test

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func linuxBuildFixture(t *testing.T, arch string) buildFixture {
	t.Helper()
	f := newBuildFixture(t)
	f.goos, f.goarch = "linux", arch
	f.provenance["platform"] = "linux"
	f.provenance["architecture"] = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
	f.source.OpenGrep.Artifacts[0].GOOS, f.source.OpenGrep.Artifacts[0].GOARCH = "linux", arch
	pythonHash := strings.Repeat("e", 64)
	runtimeLock := buildJSON(t, map[string]any{"linux": map[string]any{
		"glibc_max": "2.35", "python": map[string]string{"sha256": pythonHash}}})
	f.archiveFiles["inputs/locks/runtimes.json"] = runtimeLock
	var lock map[string]any
	testutil.FailErr(t, "decode Linux source lock", json.Unmarshal(f.archiveFiles["inputs/source-lock.json"], &lock))
	lock["files"].(map[string]any)["locks/runtimes.json"] = buildDigest(runtimeLock)
	lockBytes := buildJSON(t, lock)
	f.archiveFiles["inputs/source-lock.json"] = lockBytes
	f.source.OpenGrep.SourceLockSHA256 = buildDigest(lockBytes)
	f.provenance["source_lock_sha256"] = buildDigest(lockBytes)
	writeBuildFile(t, f.directory, "source-lock.json", lockBytes)

	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], 2)
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(header[18:], machine)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	f.replacePayload(t, "opengrep", "binary", header)
	image := func(name string) map[string]any {
		return map[string]any{"path": name, "sha256": buildDigest(header), "architecture": arch,
			"glibc_versions": []string{"2.17", "2.34"}, "dependencies": []string{"libc.so.6"}, "rpaths": []string{}}
	}
	standalone := []any{image("opengrep.bin"), image("semgrep/bin/opengrep-core")}
	report := map[string]any{"schema_version": 1, "platform": "linux", "architecture": arch, "glibc_max": "2.35",
		"outer": image("opengrep"), "standalone": standalone, "extracted": standalone}
	f.replacePayload(t, "platform-checks.json", "platform_checks", buildJSON(t, report))
	f.replacePayload(t, "runtime.json", "runtime", buildJSON(t, map[string]any{
		"python_runtime": map[string]string{"source_sha256": pythonHash}}))
	f.writeArchive(t, true)
	return f
}

func TestLinuxArtifactAdmissionChecksSourceAndExecutable(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			f := linuxBuildFixture(t, arch)
			f.selectArtifact(t)
			f.replacePayload(t, "runtime.json", "runtime", buildJSON(t, map[string]any{
				"python_runtime": map[string]string{"source_sha256": strings.Repeat("f", 64)}}))
			if _, err := bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch); err == nil {
				t.Fatal("Linux artifact with a different Python source was admitted")
			}
		})
	}
}

func TestLinuxArtifactAdmissionAllowsOnlySystemRuntimeLibraries(t *testing.T) {
	for _, tc := range []struct {
		library  string
		admitted bool
	}{
		{library: "libgcc_s.so.1", admitted: true},
		{library: "libstdc++.so.6", admitted: false},
	} {
		t.Run(tc.library, func(t *testing.T) {
			f := linuxBuildFixture(t, "amd64")
			raw, err := os.ReadFile(filepath.Join(f.directory, "platform-checks.json"))
			testutil.FailErr(t, "read Linux platform report", err)
			var report map[string]any
			testutil.FailErr(t, "decode Linux platform report", json.Unmarshal(raw, &report))
			for _, key := range []string{"standalone", "extracted"} {
				images := report[key].([]any)
				extension := images[0].(map[string]any)
				extension["dependencies"] = []string{tc.library, "libc.so.6"}
			}
			f.replacePayload(t, "platform-checks.json", "platform_checks", buildJSON(t, report))
			f.writeArchive(t, true)
			_, err = bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch)
			if tc.admitted && err != nil {
				t.Fatalf("Linux artifact needing %s was refused: %v", tc.library, err)
			}
			if !tc.admitted && err == nil {
				t.Fatalf("Linux artifact needing an unpackaged %s was admitted", tc.library)
			}
		})
	}
}
