package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNixBunRejectsInvalidPinsAndAmbiguousBuildOutputsBeforeConversion(t *testing.T) {
	for _, mode := range []string{"missing flake", "malformed flake", "missing revision", "non-hex revision", "relative output", "multiple outputs"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			testutil.FailErr(t, "frontend", os.Mkdir(filepath.Join(repo, "lycaon-den"), 0750))
			testutil.FailErr(t, "lock", os.WriteFile(filepath.Join(repo, "lycaon-den/bun.lock"), []byte("locked"), 0600))
			revision := strings.Repeat("a", 40)
			flake := `{"nodes":{"bun2nix":{"locked":{"rev":"` + revision + `"}}}}`
			switch mode {
			case "malformed flake":
				flake = "{"
			case "missing revision":
				flake = "{}"
			case "non-hex revision":
				flake = strings.ReplaceAll(flake, revision, strings.Repeat("z", 40))
			}
			if mode != "missing flake" {
				testutil.FailErr(t, "flake", os.WriteFile(filepath.Join(repo, "flake.lock"), []byte(flake), 0600))
			}
			calls := 0
			runner := func(_ context.Context, name string, _ ...string) ([]byte, error) {
				calls++
				if calls != 1 || name != "nix" {
					t.Fatalf("invalid input reached converter: %s", name)
				}
				if mode == "relative output" {
					return []byte("relative/path\n"), nil
				}
				return []byte(filepath.Join(repo, "one") + "\n" + filepath.Join(repo, "two") + "\n"), nil
			}
			if err := generateNixBun(t.Context(), repo, runner); err == nil {
				t.Fatal("invalid pin/output was accepted")
			}
			want := 0
			if mode == "relative output" || mode == "multiple outputs" {
				want = 1
			}
			if calls != want {
				t.Fatalf("build calls=%d want=%d", calls, want)
			}
		})
	}
}
