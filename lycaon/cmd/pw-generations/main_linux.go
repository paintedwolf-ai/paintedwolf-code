//go:build linux

// pw-generations is the package maintainer scripts' root-only owner of the
// protected launch's immutable generations and the administrator's launch
// policy. Its operations and paths are fixed; see docs/security.md, Protected
// Linux launch.
//
//	pw-generations install --family deb|rpm|pacman
//	pw-generations remove
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/launchguard"
	"github.com/lycaon/lycaon/internal/packagegeneration"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "painted-wolf-code: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runCommand(args, os.Stdout, launchguard.PolicyPath, packagegeneration.Install, packagegeneration.Remove)
}

// runCommand keeps CLI dispatch testable without changing the installed paths or
// generation owners selected by run.
func runCommand(args []string, output io.Writer, policyPath string,
	install func(string, func(packagegeneration.Generation, packagegeneration.SystemAccess) error, func() (string, error)) (packagegeneration.Generation, error),
	remove func(func() error, func() (string, error)) error,
) error {
	if len(args) == 3 {
		if args[0] != "install" || args[1] != "--family" || (args[2] != "deb" && args[2] != "rpm" && args[2] != "pacman") {
			return errors.New("usage: pw-generations install --family deb|rpm|pacman | remove")
		}
		if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil { // #nosec G301 -- root-owned public launch policy must be readable by unprivileged validators
			return err
		}
		generation, err := install(args[2], writePolicy, selection)
		if err != nil {
			return fmt.Errorf("could not install the protected launch generation: %w", err)
		}
		fmt.Fprintf(output, "painted-wolf-code: selected generation %s\n", generation.ManifestSHA256)
		return nil
	}
	if len(args) == 1 && args[0] == "remove" {
		return remove(removePolicy, selection)
	}
	return errors.New("usage: pw-generations install --family deb|rpm|pacman | remove")
}

func writePolicy(generation packagegeneration.Generation, access packagegeneration.SystemAccess) error {
	return launchguard.WritePolicy(generationPolicy(generation, access))
}

func generationPolicy(generation packagegeneration.Generation, access packagegeneration.SystemAccess) launchguard.GenerationPolicy {
	return launchguard.GenerationPolicy{
		Version: 1, GenerationRoot: generation.Root, ManifestSHA256: generation.ManifestSHA256,
		LogicalInstallRoot: generation.LogicalInstallRoot, SystemExecutables: access.Executables,
		LibraryRoots: access.LibraryRoots,
	}
}

// selection is the generation the current policy selects; none once removed.
func selection() (string, error) {
	return selectedGeneration(launchguard.ReadPolicy())
}

func selectedGeneration(policy launchguard.GenerationPolicy, err error) (string, error) {
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return policy.GenerationRoot, nil
}

// removePolicy withdraws the selection so no new launch starts; running
// launches keep the generation they hold.
func removePolicy() error {
	return withdrawPolicy(launchguard.PolicyPath)
}

func withdrawPolicy(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return removePolicyFile(path, info)
}

func removePolicyFile(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != 0 {
		return fmt.Errorf("%s is not the package's launch policy", path)
	}
	return os.Remove(path)
}
