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
	root        string
	languageDir string
}

func resolveModulePaths(rootFlag string) (modulePaths, error) {
	root, err := filepath.Abs(filepath.Clean(rootFlag))
	if err != nil {
		return modulePaths{}, fmt.Errorf("resolve module root: %w", err)
	}
	language := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "command-language-v1")
	if err := assertDir(language); err != nil {
		return modulePaths{}, fmt.Errorf("root %q is not the lycaon module (missing config/packs/painted-wolf/platform/tools/command-language-v1): %w", root, err)
	}
	return modulePaths{root: root, languageDir: language}, nil
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
