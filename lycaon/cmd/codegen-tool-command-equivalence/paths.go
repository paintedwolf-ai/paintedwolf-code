package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	codegenDirPerm  = 0o750
	codegenFilePerm = 0o600
)

// modulePaths are the only filesystem locations this codegen may read or write.
type modulePaths struct {
	root                 string
	equivalenceYAML      string
	generatedGo          string
	generatedReadHabitGo string
	toolSchemasDir       string
}

func resolveModulePaths(rootFlag string) (modulePaths, error) {
	root, err := filepath.Abs(filepath.Clean(rootFlag))
	if err != nil {
		return modulePaths{}, fmt.Errorf("resolve module root: %w", err)
	}
	equivalence := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "tool-command-equivalence.yaml")
	if err := assertRegularFile(equivalence); err != nil {
		return modulePaths{}, fmt.Errorf("root %q is not the lycaon module (missing config/packs/painted-wolf/platform/tools/tool-command-equivalence.yaml): %w", root, err)
	}
	out := modulePaths{
		root:                 root,
		equivalenceYAML:      equivalence,
		generatedGo:          filepath.Join(root, "internal", "tools", "command_equivalence_gen.go"),
		generatedReadHabitGo: filepath.Join(root, "internal", "confine", "read_habit_catalog_gen.go"),
		toolSchemasDir:       filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"),
	}
	for _, path := range []string{out.generatedGo, out.generatedReadHabitGo, out.toolSchemasDir} {
		if err := assertPathUnderRoot(path, root); err != nil {
			return modulePaths{}, err
		}
	}
	if err := assertDir(out.toolSchemasDir); err != nil {
		return modulePaths{}, fmt.Errorf("tools/schemas: %w", err)
	}
	return out, nil
}

func assertPathUnderRoot(path, root string) error {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("abs path %q: %w", path, err)
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("abs root %q: %w", root, err)
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return fmt.Errorf("rel path under %q: %w", cleanRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes root %q", cleanPath, cleanRoot)
	}
	return nil
}

func assertRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", path)
	}
	return nil
}

func assertDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	return nil
}

func readTrackedFile(path string) ([]byte, error) {
	// Paths originate from resolveModulePaths.
	return os.ReadFile(path) // #nosec G304 -- constrained to lycaon module allowlist
}

func writeTrackedFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), codegenDirPerm); err != nil {
		return err
	}
	return os.WriteFile(path, content, codegenFilePerm)
}
