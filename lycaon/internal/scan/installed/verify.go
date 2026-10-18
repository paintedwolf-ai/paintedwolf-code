// Package installed qualifies the packaged scanner through its product driver.
package installed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	bundleddriver "github.com/lycaon/lycaon/internal/scan/drivers/bundled"
	"github.com/lycaon/lycaon/pkg/api"
)

// Report distinguishes packaged identity, applied execution, and fixture outcome.
type Report struct {
	SchemaVersion     int                        `json:"schema_version"`
	Status            string                     `json:"status"`
	Code              string                     `json:"code,omitempty"`
	Error             string                     `json:"error,omitempty"`
	GOOS              string                     `json:"goos"`
	GOARCH            string                     `json:"goarch"`
	EngineRoot        string                     `json:"engine_root,omitempty"`
	EngineVersion     string                     `json:"engine_version,omitempty"`
	ProducerCommit    string                     `json:"producer_commit,omitempty"`
	Binary            string                     `json:"binary,omitempty"`
	BinarySHA256      string                     `json:"binary_sha256,omitempty"`
	BinaryBytes       int64                      `json:"binary_bytes,omitempty"`
	Confined          bool                       `json:"confined"`
	LinuxCapabilities *linuxsandbox.Capabilities `json:"linux_capabilities,omitempty"`
	Cases             []Case                     `json:"cases,omitempty"`
}

// Verify requires an explicit packaged root and never uses device caches or PATH.
func Verify(ctx context.Context, root string) (report Report, err error) {
	report = Report{SchemaVersion: 1, Status: "failed", Code: "root_invalid", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	defer func() {
		if err != nil {
			report.Error = err.Error()
		}
	}()
	if err := ctx.Err(); err != nil {
		report.Code = "canceled"
		return report, err
	}
	if !filepath.IsAbs(root) {
		return report, fmt.Errorf("installed scanner root must be absolute")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return report, err
	}
	report.EngineRoot = root
	report.Code = "artifact_refused"
	manifest, err := bundled.LoadManifest()
	if err != nil {
		return report, err
	}
	report.EngineVersion = manifest.OpenGrep.Version
	if manifest.OpenGrepSelection != nil {
		report.ProducerCommit = manifest.OpenGrepSelection.ProducerCommit
	}
	report.Binary, err = bundled.VerifyStagedArtifact(manifest, root, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return report, err
	}
	identity, err := manifest.ArtifactForCurrentPlatform()
	if err != nil {
		return report, err
	}
	report.BinarySHA256, report.BinaryBytes = identity.SHA256, identity.BinaryBytes
	if caps, linux := confine.LinuxCapabilities(); linux {
		report.LinuxCapabilities = &caps
	}
	if !confine.Enforcing() {
		report.Status, report.Code = "unverified", "confinement_unavailable"
		return report, fmt.Errorf("installed scanner qualification requires applied command confinement")
	}
	report.Code = "fixture_setup_failed"
	project, err := os.MkdirTemp("", "paintedwolf-installed-scan-")
	if err != nil {
		return report, err
	}
	defer func() { _ = os.RemoveAll(project) }()
	for _, fixture := range fixtures {
		if err := os.WriteFile(filepath.Join(project, fixture.file), []byte(fixture.source), 0o600); err != nil {
			return report, err
		}
	}
	report.Code = "scan_failed"
	scanner := bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{ID: "installed-qualification", Manifest: manifest, EngineRoot: root, Jobs: 1})
	result, err := scanner.Run(ctx, scan.ScanRequest{ProjectDir: project, Categories: []api.ScanCategory{api.ScanCategorySAST}})
	if err != nil {
		return report, err
	}
	report.Confined = true
	report.Code = "fixture_mismatch"
	report.Cases, err = verifyResult(project, result)
	if err != nil {
		return report, err
	}
	if _, err := bundled.VerifyStagedArtifact(manifest, root, runtime.GOOS, runtime.GOARCH); err != nil {
		report.Code = "artifact_refused"
		return report, err
	}
	report.Status, report.Code = "passed", ""
	return report, nil
}
