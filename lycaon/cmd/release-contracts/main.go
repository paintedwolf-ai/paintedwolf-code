// Command release-contracts seals workflows and verifies release contracts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/releasecontract"
)

func main() {
	moduleRoot := configlayout.FindModuleRoot()
	if moduleRoot == "" {
		fmt.Fprintln(os.Stderr, "release-contracts: run inside a checkout")
		os.Exit(1)
	}
	os.Exit(run(context.Background(), filepath.Dir(moduleRoot), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, repoRoot string, argv []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("release-contracts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	checkOnly := flags.Bool("check", false, "verify the archive matches the tag without writing")
	sourceRoot := flags.String("repo-root", repoRoot, "candidate checkout to inspect")
	baseline := flags.String("baseline", "", "stable release commit to compare")
	published := flags.String("published-versions", "[]", "published post-v1 versions whose migration identities must survive")
	checkAll := flags.Bool("check-all", false, "verify catalog contracts against a stable release")
	if err := flags.Parse(argv); err != nil {
		return 2
	}
	repoRoot = *sourceRoot
	if *checkAll && !*checkOnly && flags.NArg() == 0 {
		var versions []string
		if err := json.Unmarshal([]byte(*published), &versions); err != nil || versions == nil {
			fmt.Fprintln(stderr, "release migrations: published versions must be a JSON array")
			return 1
		}
		if err := releasecontract.CheckCompatibility(ctx, repoRoot, *baseline, stdout); err != nil {
			fmt.Fprintf(stderr, "release compatibility: %v\n", err)
			return 1
		}
		if err := releasecontract.CheckPublishedMigrations(ctx, repoRoot, versions); err != nil {
			fmt.Fprintf(stderr, "release migrations: %v\n", err)
			return 1
		}
		return 0
	}
	if *checkAll || flags.NArg() != 3 {
		fmt.Fprintln(stderr, "usage: release-contracts --check-all | [--check] <pack> <workflow> <tag>")
		fmt.Fprintln(stderr, "example: release-contracts painted-wolf/security-survey security-survey v1.0.0")
		return 2
	}
	args := flags.Args()
	report, err := releasecontract.Seal(ctx, repoRoot, args[0], args[1], args[2], *checkOnly)
	if err != nil {
		fmt.Fprintf(stderr, "release-contracts: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, report)
	return 0
}
