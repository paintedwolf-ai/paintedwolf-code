// Command codegen-dependency-inventory projects the dependency policy under
// dependencies/, the pins in the tree, and the upstream snapshot into the
// dependency inventory page and the Dependabot configuration.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	policyDir     = "dependencies"
	snapshotRel   = "dependencies/upstream.json"
	inventoryRel  = "docs/operations/dependency-inventory.md"
	dependabotRel = ".github/dependabot.yml"
)

func main() {
	repo := flag.String("repo", "..", "repository root")
	check := flag.Bool("check", false, "fail when the policy no longer matches the manifests or .github/dependabot.yml is stale")
	refresh := flag.Bool("refresh-upstream", false, "query package registries and rewrite the upstream snapshot")
	flag.Parse()
	if *check && *refresh {
		fmt.Fprintln(os.Stderr, "codegen-dependency-inventory: --check and --refresh-upstream are exclusive")
		os.Exit(2)
	}
	if err := run(*repo, *check, *refresh); err != nil {
		fmt.Fprintln(os.Stderr, "codegen-dependency-inventory:", err)
		os.Exit(1)
	}
}

func run(repo string, check, refresh bool) error {
	p, err := loadPolicy(filepath.Join(repo, policyDir))
	if err != nil {
		return err
	}
	sections, err := collect(repo, p)
	if err != nil {
		return err
	}
	if check {
		// The inventory page refreshes on its own schedule; only Dependabot's
		// projection of the policy has to match what is committed.
		return verify(repo, dependabotRel, renderDependabot(p, sections))
	}
	if refresh {
		snap, err := refreshSnapshot(context.Background(), sections, time.Now())
		if err != nil {
			return fmt.Errorf("refresh upstream: %w", err)
		}
		body, err := snap.encode()
		if err != nil {
			return err
		}
		if err := write(repo, snapshotRel, body); err != nil {
			return err
		}
	}
	snap, err := loadSnapshot(filepath.Join(repo, snapshotRel))
	if err != nil {
		return err
	}
	outputs := map[string][]byte{
		inventoryRel:  renderInventory(p, sections, snap),
		dependabotRel: renderDependabot(p, sections),
	}
	paths := make([]string, 0, len(outputs))
	for rel := range outputs {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	var errs []error
	for _, rel := range paths {
		errs = append(errs, write(repo, rel, outputs[rel]))
	}
	return errors.Join(errs...)
}

func verify(repo, rel string, body []byte) error {
	current, err := os.ReadFile(filepath.Join(repo, rel)) // #nosec G304 -- fixed generated path
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	if !bytes.Equal(current, body) {
		return fmt.Errorf("%s is stale; run ./task codegen:dependency-inventory", rel)
	}
	return nil
}

func write(repo, rel string, body []byte) error {
	path := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil { // #nosec G306 -- generated artifact
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}
