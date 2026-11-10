package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSlugProjectName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"lycaon", "lycaon"},
		{"Deal finder", "deal-finder"},
		{"  Foo--Bar!!  ", "foo-bar"},
		{"", "root"},
	}
	for _, tc := range tests {
		if got := SlugProjectName(tc.in); got != tc.want {
			t.Fatalf("SlugProjectName(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestDerivedRootLabelPreservesFolderNameAndBoundsCollisionSuffix(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "Painted Wolf")
	b := filepath.Join(dir, "other", "Painted Wolf")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	first := deriveUniqueRootLabel(nil, a)
	second := deriveUniqueRootLabel([]string{strings.ToLower(first)}, b)
	if first != "Painted Wolf" {
		t.Fatalf("first = %q want Painted Wolf", first)
	}
	if second != "Painted Wolf-2" {
		t.Fatalf("second = %q want Painted Wolf-2", second)
	}

	long := strings.Repeat("界", MaxRootDisplayLabelRunes)
	bounded := deriveUniqueRootLabel([]string{long}, filepath.Join(dir, long))
	if utf8.RuneCountInString(bounded) != MaxRootDisplayLabelRunes {
		t.Fatalf("bounded label length = %d want %d", utf8.RuneCountInString(bounded), MaxRootDisplayLabelRunes)
	}
	if !strings.HasSuffix(bounded, "-2") {
		t.Fatalf("bounded label = %q want collision suffix", bounded)
	}
}

func TestDerivedRootLabelSanitizesAddressingDelimiters(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "@backend", want: "backend"},
		{name: "line\nbreak", want: "line-break"},
		{name: "tab\tbreak", want: "tab-break"},
		{name: "back\\slash", want: "back-slash"},
	} {
		if got := deriveUniqueRootLabel(nil, filepath.Join(dir, tc.name)); got != tc.want {
			t.Fatalf("deriveUniqueRootLabel(%q) = %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestResolveExistingDirAdmitsAnyExistingDirectory(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	cfgRoot, err := configdir.Root()
	testutil.FailErr(t, "resolve configdir root", err)

	candidates := []string{
		home,
		filepath.Join(home, ".config"),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, "Library", "Keychains"),
		cfgRoot,
		filepath.Join(home, "work", "repo"),
	}
	for _, dir := range candidates {
		testutil.FailErr(t, "mkdir candidate", os.MkdirAll(dir, 0o700))
		got, err := ResolveExistingDir(dir)
		if err != nil {
			t.Fatalf("ResolveExistingDir(%q) unexpected error: %v", dir, err)
		}
		want, err := filepath.EvalSymlinks(dir)
		testutil.FailErr(t, "eval symlinks", err)
		if got != want {
			t.Fatalf("ResolveExistingDir(%q) = %q, want %q", dir, got, want)
		}
	}

	rootResolved, err := ResolveExistingDir(string(filepath.Separator))
	if err != nil {
		t.Fatalf("ResolveExistingDir(root) unexpected error: %v", err)
	}
	if rootResolved == "" {
		t.Fatal("ResolveExistingDir(root) returned empty path")
	}
}

func TestResolveExistingDirRejectsInvalidMissingAndFilePaths(t *testing.T) {
	if _, err := ResolveExistingDir("   "); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("empty path err = %v, want ErrInvalidPath", err)
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := ResolveExistingDir(missing); !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("missing path err = %v, want ErrPathNotFound", err)
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	testutil.FailErr(t, "write file", os.WriteFile(file, []byte("x"), 0o600))
	if _, err := ResolveExistingDir(file); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file path err = %v, want ErrNotDirectory", err)
	}
}
