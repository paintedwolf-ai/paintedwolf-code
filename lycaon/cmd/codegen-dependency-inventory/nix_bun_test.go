package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPinnedBunGenerationPreservesInputsAndPublishesMatchedIdentity(t *testing.T) {
	for _, mode := range []string{"success", "build failure", "convert failure", "empty output", "input changed"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			testutil.FailErr(t, "create frontend", os.Mkdir(filepath.Join(repo, "lycaon-den"), 0755))
			testutil.FailErr(t, "create Nix package directory", os.MkdirAll(filepath.Join(repo, "packaging/nix"), 0755))
			lock := []byte("locked packages\n")
			lockPath := filepath.Join(repo, "lycaon-den/bun.lock")
			testutil.FailErr(t, "write lock", os.WriteFile(lockPath, lock, 0600))
			revision := strings.Repeat("a", 40)
			flake := `{"nodes":{"bun2nix":{"locked":{"rev":"` + revision + `"}}}}`
			testutil.FailErr(t, "write flake", os.WriteFile(filepath.Join(repo, "flake.lock"), []byte(flake), 0600))
			expressionPath := filepath.Join(repo, "packaging/nix/bun.nix")
			identityPath := filepath.Join(repo, "packaging/nix/bun-lock.json")
			testutil.FailErr(t, "write prior expression", os.WriteFile(expressionPath, []byte("prior expression"), 0600))
			testutil.FailErr(t, "write prior identity", os.WriteFile(identityPath, []byte("prior identity"), 0600))
			converter := filepath.Join(repo, "pinned-converter")
			calls := 0
			runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					if name != "nix" || !slices.Contains(args, "github:nix-community/bun2nix/"+revision) {
						t.Fatalf("unlocked converter invocation: %s %q", name, args)
					}
					if mode == "build failure" {
						return nil, errors.New("build failed")
					}
					return []byte(converter + "\n"), nil
				}
				if name != filepath.Join(converter, "bin/bun2nix") || len(args) != 4 || args[0] != "--lock-file" || args[1] != lockPath || args[2] != "--output-file" {
					t.Fatalf("converter invocation: %s %q", name, args)
				}
				if mode == "convert failure" {
					return nil, errors.New("conversion failed")
				}
				if mode == "input changed" {
					testutil.FailErr(t, "change input during conversion", os.WriteFile(lockPath, []byte("new input"), 0600))
				}
				expression := []byte("generated expression\n")
				if mode == "empty output" {
					expression = nil
				}
				return nil, os.WriteFile(args[3], expression, 0600)
			}
			err := generateNixBun(t.Context(), repo, runner)
			expression, readErr := os.ReadFile(expressionPath)
			testutil.FailErr(t, "read expression", readErr)
			identity, readErr := os.ReadFile(identityPath)
			testutil.FailErr(t, "read identity", readErr)
			if mode != "success" {
				if err == nil || string(expression) != "prior expression" || string(identity) != "prior identity" {
					t.Fatalf("failed conversion published outputs: err=%v expression=%q identity=%q", err, expression, identity)
				}
				return
			}
			testutil.FailErr(t, "generate pins", err)
			var got nixBunIdentity
			testutil.FailErr(t, "decode identity", json.Unmarshal(identity, &got))
			sourceHash, expressionHash := sha256.Sum256(lock), sha256.Sum256(expression)
			if got.Source != hex.EncodeToString(sourceHash[:]) || got.Expression != hex.EncodeToString(expressionHash[:]) || got.Generator != revision {
				t.Fatalf("generated identity does not bind all inputs: %+v", got)
			}
		})
	}
}
