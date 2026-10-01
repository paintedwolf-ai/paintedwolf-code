package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/scan/bundled"
)

func main() {
	result, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "opengrep-artifact:", err)
		os.Exit(1)
	}
	fmt.Println(result)
}

type options struct {
	mode, root, target, directory, executable, digest   string
	cacheRoot, archive, releaseManifest, manifestOutput string
	tag, expectedCommit                                 string
	evidenceRoot, auditOutput, packagedDirectory        string
	offline                                             bool
	dirOnly                                             bool
}

func run() (string, error) {
	var opts options
	flag.StringVar(&opts.mode, "mode", "stage", "fetch, select, audit, identity, stage, verify, or resolve the exact artifact")
	flag.StringVar(&opts.root, "root", "", "artifact staging root")
	flag.StringVar(&opts.target, "target", "", "Rust target triple; defaults to the host")
	flag.StringVar(&opts.directory, "artifact-directory", "", "absolute verified prebuilt artifact directory")
	flag.StringVar(&opts.executable, "executable", "", "explicit development evaluation executable")
	flag.StringVar(&opts.digest, "sha256", "", "explicit development evaluation digest")
	flag.BoolVar(&opts.dirOnly, "dir-only", false, "print the executable directory")
	flag.StringVar(&opts.cacheRoot, "cache-root", "", "verified release cache root")
	flag.StringVar(&opts.archive, "archive", "", "import an exact local release archive")
	flag.StringVar(&opts.releaseManifest, "release-manifest", "", "local JSON or HTTPS release descriptor")
	flag.StringVar(&opts.manifestOutput, "manifest-output", "", "selected release YAML output")
	flag.StringVar(&opts.tag, "tag", "", "exact published release tag for authenticated selection")
	flag.StringVar(&opts.expectedCommit, "expected-commit", "", "independently reviewed full source commit for selection")
	flag.StringVar(&opts.evidenceRoot, "evidence-root", "", "committed scanner configuration directory for offline audit")
	flag.StringVar(&opts.auditOutput, "audit-output", "", "application release audit output")
	flag.StringVar(&opts.packagedDirectory, "packaged-directory", "", "engine directory verified inside the application package")
	flag.BoolVar(&opts.offline, "offline", false, "use only imported or cached release bytes")
	flag.Parse()
	if flag.NArg() != 0 {
		return "", fmt.Errorf("unexpected positional arguments")
	}
	return execute(opts)
}

func execute(opts options) (string, error) {
	if err := opts.validate(); err != nil {
		return "", err
	}
	if opts.mode == "select" {
		return selectRelease(opts)
	}
	m, err := bundled.LoadManifest()
	if err != nil {
		return "", err
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if opts.target != "" {
		goos, goarch, err = bundled.PlatformForTarget(opts.target)
		if err != nil {
			return "", err
		}
	}
	if opts.mode == "fetch" {
		resolved, err := bundled.FetchReleaseArtifact(context.Background(), m, goos, goarch, opts.fetchOptions())
		if err != nil {
			return "", err
		}
		return resolved.Directory, nil
	}
	if opts.directory != "" {
		m, err = bundled.SelectReleaseArtifact(context.Background(), m, opts.directory, goos, goarch)
		if err != nil {
			return "", err
		}
	}
	if opts.mode == "identity" {
		return bundled.EncodedBuildIdentity(m)
	}
	if opts.mode == "audit" {
		if err := bundled.WriteReleaseAudit(context.Background(), m, opts.packagedDirectory, opts.evidenceRoot, opts.auditOutput); err != nil {
			return "", err
		}
		return filepath.Abs(opts.auditOutput)
	}
	var path string
	if opts.executable != "" {
		path, err = verifyExplicit(m, opts, goos, goarch)
	} else {
		root, rootErr := filepath.Abs(opts.root)
		if rootErr != nil {
			return "", rootErr
		}
		if opts.mode == "verify" {
			path, err = bundled.VerifyStagedArtifact(m, root, goos, goarch)
		} else {
			path, err = bundled.StageArtifact(context.Background(), m, root, goos, goarch)
		}
	}
	if err != nil {
		return "", err
	}
	if opts.dirOnly {
		return filepath.Dir(path), nil
	}
	return path, nil
}

func (opts options) validate() error {
	if !configdir.IsDevelopmentBuild() && (opts.executable != "" || opts.digest != "") {
		return fmt.Errorf("independent executable overrides are unavailable in release builds")
	}
	switch opts.mode {
	case "identity", "stage", "resolve", "verify", "fetch", "select", "audit":
	default:
		return fmt.Errorf("invalid mode: %s", opts.mode)
	}
	if opts.mode != "audit" && (opts.evidenceRoot != "" || opts.auditOutput != "" || opts.packagedDirectory != "") {
		return fmt.Errorf("evidence-root, packaged-directory and audit-output require audit mode")
	}
	if os.Getenv(bundled.EnvOpenGrepCandidate) != "" && (opts.executable != "" || opts.digest != "" || opts.directory != "" || opts.mode == "identity") {
		return fmt.Errorf("independent artifact identities cannot be combined with %s", bundled.EnvOpenGrepCandidate)
	}
	if opts.mode == "fetch" || opts.mode == "select" {
		return opts.validateRelease()
	}
	if opts.cacheRoot != "" || opts.archive != "" || opts.offline || opts.releaseManifest != "" || opts.manifestOutput != "" || opts.tag != "" || opts.expectedCommit != "" {
		return fmt.Errorf("release options require fetch or select mode")
	}
	if opts.mode == "audit" {
		if opts.directory == "" || opts.evidenceRoot == "" || opts.auditOutput == "" || opts.packagedDirectory == "" || opts.root != "" || opts.executable != "" || opts.digest != "" || opts.dirOnly {
			return fmt.Errorf("audit requires artifact-directory, packaged-directory, evidence-root and audit-output")
		}
		return nil
	}
	if opts.mode == "identity" {
		if opts.directory == "" || opts.root != "" || opts.executable != "" || opts.digest != "" || opts.dirOnly {
			return fmt.Errorf("identity requires only artifact-directory and optional target")
		}
		return nil
	}
	if opts.mode == "verify" && opts.directory != "" {
		return fmt.Errorf("verify uses embedded build identity, not artifact-directory metadata")
	}
	if opts.executable != "" {
		if opts.mode == "stage" || opts.directory != "" {
			return fmt.Errorf("explicit executable is only for independent evaluation verification")
		}
		return nil
	}
	if opts.digest != "" {
		return fmt.Errorf("sha256 override requires an explicit executable")
	}
	if opts.root == "" {
		return fmt.Errorf("artifact root required")
	}
	return nil
}

func verifyExplicit(m *bundled.Manifest, opts options, goos, goarch string) (string, error) {
	path, err := filepath.Abs(opts.executable)
	if err != nil {
		return "", err
	}
	digest := opts.digest
	if digest == "" {
		art, err := m.ArtifactForPlatform(goos, goarch)
		if err != nil {
			return "", err
		}
		digest = art.SHA256
	}
	if err := bundled.VerifyFileSHA256(path, digest); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if goos != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("artifact is not executable: %s", path)
	}
	return path, nil
}
