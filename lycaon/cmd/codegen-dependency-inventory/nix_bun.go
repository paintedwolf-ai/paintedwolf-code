package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type nixBunIdentity struct {
	Source     string `json:"source_sha256"`
	Expression string `json:"expression_sha256"`
	Generator  string `json:"generator_revision"`
}

type bunCommand func(context.Context, string, ...string) ([]byte, error)

func generateNixBun(ctx context.Context, repo string, run bunCommand) error {
	root, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(root, "lycaon-den/bun.lock")
	source, err := os.ReadFile(lockPath) // #nosec G304 -- fixed input or owned generator staging path
	if err != nil {
		return err
	}
	flakePath := filepath.Join(root, "flake.lock")
	flake, err := os.ReadFile(flakePath) // #nosec G304 -- fixed input or owned generator staging path
	if err != nil {
		return err
	}
	var inputs struct {
		Nodes map[string]struct {
			Locked struct {
				Rev string `json:"rev"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(flake, &inputs); err != nil {
		return err
	}
	revision := inputs.Nodes["bun2nix"].Locked.Rev
	if len(revision) != 40 {
		return fmt.Errorf("flake.lock has no pinned bun2nix revision")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return fmt.Errorf("invalid bun2nix revision: %w", err)
	}
	output, err := run(ctx, "nix", "--extra-experimental-features", "nix-command flakes", "build", "--no-link", "--print-out-paths", "github:nix-community/bun2nix/"+revision)
	if err != nil {
		return fmt.Errorf("build pinned bun2nix: %w", err)
	}
	outputs := strings.Fields(string(output))
	if len(outputs) != 1 || !filepath.IsAbs(outputs[0]) {
		return fmt.Errorf("expected one absolute bun2nix output, got %q", output)
	}
	target := filepath.Join(root, "packaging/nix/bun.nix")
	stage, err := os.CreateTemp(filepath.Dir(target), ".bun-nix-*")
	if err != nil {
		return err
	}
	stageName := stage.Name()
	defer func() { _ = os.Remove(stageName) }()
	if err := stage.Close(); err != nil {
		return err
	}
	_, err = run(ctx, filepath.Join(outputs[0], "bin/bun2nix"), "--lock-file", lockPath, "--output-file", stageName)
	if err != nil {
		return fmt.Errorf("convert Bun lockfile: %w", err)
	}
	expression, err := os.ReadFile(stageName) // #nosec G304 -- fixed input or owned generator staging path
	if err != nil {
		return err
	}
	if len(expression) == 0 {
		return fmt.Errorf("bun2nix produced an empty expression")
	}
	current, err := os.ReadFile(lockPath) // #nosec G304 -- fixed input or owned generator staging path
	if err != nil {
		return err
	}
	currentFlake, err := os.ReadFile(flakePath) // #nosec G304 -- fixed input or owned generator staging path
	if err != nil {
		return err
	}
	if string(current) != string(source) || string(currentFlake) != string(flake) {
		return fmt.Errorf("dependency pins changed during generation; rerun generation")
	}
	sourceHash, expressionHash := sha256.Sum256(source), sha256.Sum256(expression)
	identity := nixBunIdentity{Source: hex.EncodeToString(sourceHash[:]), Expression: hex.EncodeToString(expressionHash[:]), Generator: revision}
	body, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	if err := os.Chmod(stageName, 0600); err != nil {
		return err
	}
	if err := os.Rename(stageName, target); err != nil {
		return err
	}
	return write(root, "packaging/nix/bun-lock.json", append(body, '\n'))
}

func runBunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "nix" {
		binary, err := exec.LookPath(name)
		if err != nil {
			binary = "/nix/var/nix/profiles/default/bin/nix"
		}
		name = binary
	}
	command := exec.CommandContext(ctx, name, args...) // #nosec G204 -- pinned converter and structured generator arguments
	command.Stderr = os.Stderr
	return command.Output()
}
