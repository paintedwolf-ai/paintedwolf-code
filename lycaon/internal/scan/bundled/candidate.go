package bundled

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/configdir"
)

// EnvOpenGrepCandidate selects a local build artifact in development binaries.
const EnvOpenGrepCandidate = "LYCAON_OPENGREP_CANDIDATE"

type localCandidate struct {
	directory        string
	provenanceSHA256 string
	provenance       candidateProvenance
	lock             candidateSourceLock
}

func loadCandidate(directory string) (*Manifest, error) {
	if !configdir.IsDevelopmentBuild() {
		return nil, fmt.Errorf("%s is unavailable in release builds", EnvOpenGrepCandidate)
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("%s must name an absolute artifact directory", EnvOpenGrepCandidate)
	}
	candidate, err := readCandidate(filepath.Clean(directory), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("opengrep candidate: %w", err)
	}
	m := &Manifest{candidate: candidate, OpenGrep: OpenGrepManifest{
		Version: candidate.provenance.Version, Origin: "downstream",
		UpstreamVersion: candidate.lock.UpstreamVersion, BaseRevision: candidate.lock.Revision,
		Revision: candidate.lock.PatchVersion, SourceLockSHA256: candidate.provenance.SourceLockSHA256,
		License: "LGPL-2.1",
	}}
	if err := ValidateManifest(m); err != nil {
		return nil, fmt.Errorf("opengrep candidate identity: %w", err)
	}
	if err := candidate.verify(); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *localCandidate) binaryPath() string { return BinaryPath(c.directory) }

func (c *localCandidate) validateManifest(engine OpenGrepManifest) error {
	if !configdir.IsDevelopmentBuild() {
		return fmt.Errorf("local opengrep candidates are unavailable in release builds")
	}
	if engine.Origin != "downstream" || engine.Version != c.provenance.Version ||
		engine.UpstreamVersion != c.lock.UpstreamVersion || engine.BaseRevision != c.lock.Revision ||
		engine.Revision != c.lock.PatchVersion || engine.SourceLockSHA256 != c.provenance.SourceLockSHA256 ||
		engine.License != "LGPL-2.1" {
		return fmt.Errorf("local opengrep candidate manifest differs from its verified identity")
	}
	return nil
}

func (c *localCandidate) verify() error {
	if err := verifyCandidateMetadata(filepath.Join(c.directory, "provenance.json"), c.provenanceSHA256); err != nil {
		return fmt.Errorf("opengrep candidate provenance: %w", err)
	}
	if err := verifyCandidateMetadata(filepath.Join(c.directory, "source-lock.json"), c.provenance.SourceLockSHA256); err != nil {
		return fmt.Errorf("opengrep candidate source lock: %w", err)
	}
	info, err := candidateRegularFile(c.binaryPath())
	if err != nil {
		return fmt.Errorf("opengrep candidate executable: %w", err)
	}
	if info.Size() != c.provenance.BinaryBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("opengrep candidate executable size or permissions differ")
	}
	if err := VerifyFileSHA256(c.binaryPath(), c.provenance.BinarySHA256); err != nil {
		return fmt.Errorf("%w: opengrep candidate: %w", ErrOpenGrepChecksum, err)
	}
	return nil
}
