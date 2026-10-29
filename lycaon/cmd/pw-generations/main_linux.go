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
	switch {
	case len(args) == 3 && args[0] == "install" && args[1] == "--family" && (args[2] == "deb" || args[2] == "rpm" || args[2] == "pacman"):
		if err := os.MkdirAll(filepath.Dir(launchguard.PolicyPath), 0o755); err != nil { // #nosec G301 -- root-owned public launch policy must be readable by unprivileged validators
			return err
		}
		generation, err := packagegeneration.Install(args[2], writePolicy, selection)
		if err != nil {
			return fmt.Errorf("could not install the protected launch generation: %w", err)
		}
		fmt.Printf("painted-wolf-code: selected generation %s\n", generation.ManifestSHA256)
		return nil
	case len(args) == 1 && args[0] == "remove":
		return packagegeneration.Remove(removePolicy, selection)
	}
	return errors.New("usage: pw-generations install --family deb|rpm|pacman | remove")
}

func writePolicy(generation packagegeneration.Generation, access packagegeneration.SystemAccess) error {
	return launchguard.WritePolicy(launchguard.GenerationPolicy{
		Version: 1, GenerationRoot: generation.Root, ManifestSHA256: generation.ManifestSHA256,
		LogicalInstallRoot: generation.LogicalInstallRoot, SystemExecutables: access.Executables,
		LibraryRoots: access.LibraryRoots,
	})
}

// selection is the generation the current policy selects; none once removed.
func selection() (string, error) {
	policy, err := launchguard.ReadPolicy()
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
	info, err := os.Lstat(launchguard.PolicyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != 0 {
		return fmt.Errorf("%s is not the package's launch policy", launchguard.PolicyPath)
	}
	return os.Remove(launchguard.PolicyPath)
}
