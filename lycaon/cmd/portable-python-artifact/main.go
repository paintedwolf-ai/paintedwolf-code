package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "portable-python-artifact: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("portable-python-artifact", flag.ContinueOnError)
	mode := fs.String("mode", "stage", "select, fetch, stage, verify, identity, or resolve")
	manifestPath := fs.String("manifest", "", "path to bundled-manifest.yaml (defaults to embedded)")
	descriptor := fs.String("descriptor", "", "local path or HTTPS URL to PY07 release descriptor JSON")
	tag := fs.String("tag", "", "authenticated PY07 release tag")
	expectedCommit := fs.String("expected-commit", "", "expected 40-char PY07 producer git commit")
	root := fs.String("root", "", "engine stage root directory")
	cacheDir := fs.String("cache-dir", "", "local cache directory for fetched native engine artifacts")
	target := fs.String("target", "", "bundle target triple (e.g. aarch64-apple-darwin, x86_64-unknown-linux-gnu)")
	goos := fs.String("goos", runtime.GOOS, "target GOOS")
	goarch := fs.String("goarch", runtime.GOARCH, "target GOARCH")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedGOOS, resolvedGOARCH := *goos, *goarch
	if strings.TrimSpace(*target) != "" {
		var err error
		resolvedGOOS, resolvedGOARCH, err = pythonruntime.EnginePlatformForTarget(*target)
		if err != nil {
			return err
		}
	}

	ctx := context.Background()
	switch *mode {
	case "select":
		if strings.TrimSpace(*manifestPath) == "" {
			return fmt.Errorf("-manifest is required for select mode")
		}
		m, err := pythonruntime.SelectEngineRelease(ctx, pythonruntime.SelectEngineReleaseRequest{
			DescriptorSource: *descriptor,
			Tag:              *tag,
			ExpectedCommit:   *expectedCommit,
			ManifestPath:     *manifestPath,
		})
		if err != nil {
			return err
		}
		fmt.Printf("selected portable-python native engine %s (tag=%s commit=%s)\n", m.Engine.Version, m.Selection.Tag, m.Selection.ProducerCommit)
		return nil

	case "fetch", "resolve":
		m, err := loadManifest(*manifestPath)
		if err != nil {
			return err
		}
		outDir := strings.TrimSpace(*cacheDir)
		if outDir == "" {
			return fmt.Errorf("-cache-dir is required for %s mode", *mode)
		}
		platformDir := filepath.Join(outDir, m.Engine.Version, resolvedGOOS+"-"+resolvedGOARCH)
		dir, err := pythonruntime.FetchEngineArtifact(ctx, m, resolvedGOOS, resolvedGOARCH, platformDir, nil)
		if err != nil {
			return err
		}
		fmt.Println(dir)
		return nil

	case "stage":
		m, err := loadManifest(*manifestPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return fmt.Errorf("-root is required for stage mode")
		}
		srcDir := strings.TrimSpace(*cacheDir)
		if srcDir == "" {
			return fmt.Errorf("-cache-dir is required for stage mode")
		}
		// Accept either the platform-specific dir or cache root.
		art, err := m.ArtifactForPlatform(resolvedGOOS, resolvedGOARCH)
		if err != nil {
			return err
		}
		if _, statErr := os.Stat(filepath.Join(srcDir, art.LibraryName)); statErr != nil {
			srcDir = filepath.Join(srcDir, m.Engine.Version, resolvedGOOS+"-"+resolvedGOARCH)
		}
		libPath, err := pythonruntime.StageEngineArtifact(ctx, m, srcDir, *root, resolvedGOOS, resolvedGOARCH)
		if err != nil {
			return err
		}
		fmt.Println(libPath)
		return nil

	case "verify":
		m, err := loadManifest(*manifestPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return fmt.Errorf("-root is required for verify mode")
		}
		libPath, err := pythonruntime.VerifyStagedEngineArtifact(m, *root, resolvedGOOS, resolvedGOARCH)
		if err != nil {
			return err
		}
		fmt.Println(libPath)
		return nil

	case "identity":
		m, err := loadManifest(*manifestPath)
		if err != nil {
			return err
		}
		encoded, err := m.EncodedBuildIdentity(resolvedGOOS, resolvedGOARCH)
		if err != nil {
			return err
		}
		fmt.Println(encoded)
		return nil

	default:
		return fmt.Errorf("unsupported mode %q", *mode)
	}
}

func loadManifest(path string) (*pythonruntime.BundledEngineManifest, error) {
	if strings.TrimSpace(path) != "" {
		return pythonruntime.LoadBundledEngineManifestFile(path)
	}
	return pythonruntime.LoadBundledEngineManifest()
}
