// Command codegen-workflow-archive extracts a released workflow and its referenced units
// byte-for-byte from a git release tag into a sealed archive directory with SHA256SUMS.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type manifestInject struct {
	Render string `yaml:"render"`
}

type manifestGate struct {
	Predicate string `yaml:"predicate"`
}

func (g *manifestGate) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		g.Predicate = node.Value
		return nil
	}
	var obj struct {
		Predicate string `yaml:"predicate"`
	}
	if err := node.Decode(&obj); err != nil {
		return err
	}
	g.Predicate = obj.Predicate
	return nil
}

type manifestPhase struct {
	ID    string         `yaml:"id"`
	Gates []manifestGate `yaml:"gates"`
}

type manifestFile struct {
	ID      string           `yaml:"id"`
	Version string           `yaml:"version"`
	Phases  []manifestPhase  `yaml:"phases"`
	Injects []manifestInject `yaml:"injects"`
}

var includeRe = regexp.MustCompile(`(?:\{%|\{\{)\s*include\s+["']([^"']+)["']`)

func main() {
	checkOnly := flag.Bool("check", false, "verify archive matches tag without writing")
	flag.Parse()

	args := flag.Args()
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: codegen-workflow-archive [--check] <pack> <workflow> <tag>\n")
		fmt.Fprintf(os.Stderr, "Example: codegen-workflow-archive painted-wolf/security-survey security-survey v1.0.0\n")
		os.Exit(2)
	}

	packID := strings.Trim(args[0], "/")
	workflowID := strings.Trim(args[1], "/")
	tag := strings.TrimSpace(args[2])

	repoRoot, err := findRepoRoot()
	if err != nil {
		fatal(fmt.Errorf("find repo root: %w", err))
	}

	// 1. Read manifest from git tag
	manifestGitRel := filepath.ToSlash(filepath.Join("lycaon", "config", "packs", packID, "workflows", workflowID, "workflow.yaml"))
	manifestBytes, err := gitShow(repoRoot, tag, manifestGitRel)
	if err != nil {
		fatal(fmt.Errorf("git show %s:%s: %w", tag, manifestGitRel, err))
	}

	var m manifestFile
	if err := yaml.Unmarshal(manifestBytes, &m); err != nil {
		fatal(fmt.Errorf("unmarshal manifest YAML: %w", err))
	}

	version := strings.TrimSpace(m.Version)
	if version == "" {
		fatal(fmt.Errorf("manifest at %s has empty version", manifestGitRel))
	}

	// 2. Validate that replay fixtures exist before sealing
	replayDir := filepath.Join(repoRoot, "lycaon", "test", "wiring", "testdata", "replay", fmt.Sprintf("%s@%s", workflowID, version))
	if entries, err := os.ReadDir(replayDir); err != nil || len(entries) == 0 {
		fatal(fmt.Errorf("refusing to seal: missing replay fixtures in %s", replayDir))
	}

	// 3. Resolve all units referenced by the manifest from the git tag
	archiveFiles := make(map[string][]byte)
	archiveFiles["workflow.yaml"] = manifestBytes

	packGuidanceGitPrefix := filepath.ToSlash(filepath.Join("lycaon", "config", "packs", packID, "guidance"))

	// Resolve injects / prompts
	for _, inj := range m.Injects {
		render := strings.TrimSpace(inj.Render)
		if render == "" {
			continue
		}
		// Look for .md then .yaml
		promptGitRelMD := filepath.ToSlash(filepath.Join(packGuidanceGitPrefix, render+".md"))
		promptGitRelYAML := filepath.ToSlash(filepath.Join(packGuidanceGitPrefix, render+".yaml"))

		var promptBytes []byte
		var promptArchiveRel string
		if data, err := gitShow(repoRoot, tag, promptGitRelMD); err == nil {
			promptBytes = data
			promptArchiveRel = filepath.ToSlash(filepath.Join("guidance", render+".md"))
		} else if data, err := gitShow(repoRoot, tag, promptGitRelYAML); err == nil {
			promptBytes = data
			promptArchiveRel = filepath.ToSlash(filepath.Join("guidance", render+".yaml"))
		} else {
			fatal(fmt.Errorf("prompt %q referenced in injects not found in tag %s", render, tag))
		}

		archiveFiles[promptArchiveRel] = promptBytes

		// Scan for included partials
		matches := includeRe.FindAllSubmatch(promptBytes, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			incName := string(match[1])
			incGitRel := filepath.ToSlash(filepath.Join(packGuidanceGitPrefix, incName))
			if incBytes, err := gitShow(repoRoot, tag, incGitRel); err == nil {
				archiveFiles[filepath.ToSlash(filepath.Join("guidance", incName))] = incBytes
			}
		}
	}

	// Resolve gate feedback files from phase gate predicates
	for _, phase := range m.Phases {
		for _, gate := range phase.Gates {
			pred := strings.TrimSpace(gate.Predicate)
			parts := strings.SplitN(pred, ":", 2)
			if len(parts) != 2 {
				continue
			}
			category, leaf := parts[0], parts[1]
			fbName := fmt.Sprintf("%s-%s.yaml", category, leaf)
			fbGitRel := filepath.ToSlash(filepath.Join(packGuidanceGitPrefix, "gate-feedback", fbName))
			if fbBytes, err := gitShow(repoRoot, tag, fbGitRel); err == nil {
				archiveFiles[filepath.ToSlash(filepath.Join("guidance", "gate-feedback", fbName))] = fbBytes
			}
		}
	}

	// 4. Compute SHA256 sums
	sortedPaths := make([]string, 0, len(archiveFiles))
	for p := range archiveFiles {
		sortedPaths = append(sortedPaths, p)
	}
	sort.Strings(sortedPaths)

	var sumsBuf bytes.Buffer
	for _, p := range sortedPaths {
		sum := sha256.Sum256(archiveFiles[p])
		fmt.Fprintf(&sumsBuf, "%s  %s\n", hex.EncodeToString(sum[:]), p)
	}
	expectedSums := sumsBuf.Bytes()

	archiveDir := filepath.Join(repoRoot, "lycaon", "config", "packs", packID, "archive", version)

	if *checkOnly {
		// Verify archive directory matches
		currentSums, err := os.ReadFile(filepath.Join(archiveDir, "SHA256SUMS"))
		if err != nil {
			fatal(fmt.Errorf("archive %s not found: %w", archiveDir, err))
		}
		if !bytes.Equal(bytes.TrimSpace(currentSums), bytes.TrimSpace(expectedSums)) {
			fatal(fmt.Errorf("archive SHA256SUMS in %s does not match tag %s", archiveDir, tag))
		}
		for _, p := range sortedPaths {
			curBytes, err := os.ReadFile(filepath.Join(archiveDir, filepath.FromSlash(p)))
			if err != nil {
				fatal(fmt.Errorf("missing file in archive %s: %s", archiveDir, p))
			}
			if !bytes.Equal(curBytes, archiveFiles[p]) {
				fatal(fmt.Errorf("file %s in %s differs from tag %s", p, archiveDir, tag))
			}
		}
		fmt.Printf("Archive %s is byte-identical to tag %s (%d files verified)\n", archiveDir, tag, len(sortedPaths))
		return
	}

	// 5. Write archive directory
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		fatal(fmt.Errorf("mkdir %s: %w", archiveDir, err))
	}

	for _, p := range sortedPaths {
		fullPath := filepath.Join(archiveDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			fatal(fmt.Errorf("mkdir parent %s: %w", fullPath, err))
		}
		if err := os.WriteFile(fullPath, archiveFiles[p], 0644); err != nil {
			fatal(fmt.Errorf("write %s: %w", fullPath, err))
		}
	}

	sumsPath := filepath.Join(archiveDir, "SHA256SUMS")
	if err := os.WriteFile(sumsPath, expectedSums, 0644); err != nil {
		fatal(fmt.Errorf("write %s: %w", sumsPath, err))
	}

	fmt.Printf("Successfully sealed %s from tag %s into %s (%d files)\n", workflowID, tag, archiveDir, len(sortedPaths))
}

func findRepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitShow(repoRoot, tag, gitRelPath string) ([]byte, error) {
	spec := fmt.Sprintf("%s:%s", tag, filepath.ToSlash(gitRelPath))
	cmd := exec.Command("git", "show", spec)
	cmd.Dir = repoRoot
	return cmd.Output()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "codegen-workflow-archive: %v\n", err)
	os.Exit(1)
}
