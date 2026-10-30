//go:build linux

package launchguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// Explicit input to the optional real-desktop leg, not a release attestation.
// den:bundle is passthrough preparation: its before/after tree and output hashes
// are retained separately from the managed runtime receipt.
type desktopArtifact struct {
	LibraryRoots            []string `json:"library_roots"`
	SystemExecutables       []string `json:"system_executables"`
	Version                 int      `json:"version"`
	Root                    string   `json:"root"`
	SourceCommit            string   `json:"source_commit"`
	SourceTree              string   `json:"source_tree"`
	BeforeTree              string   `json:"before_tree"`
	AfterTree               string   `json:"after_tree"`
	ManifestSHA256          string   `json:"manifest_sha256"`
	PreparationResult       string   `json:"preparation_result"`
	PreparationResultSHA256 string   `json:"preparation_result_sha256"`
	PreparationJob          string   `json:"preparation_job"`
	PreparationJobSHA256    string   `json:"preparation_job_sha256"`
}

var objectID = regexp.MustCompile(`^[0-9a-f]{40}$`)

func checkedBytes(path, digest string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("artifact evidence path is not absolute and clean")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("artifact evidence digest differs: %s", path)
	}
	return raw, nil
}

func loadDesktopArtifact(path, sourceTree string) (desktopArtifact, *pkgmanifest.Manifest, error) {
	var artifact desktopArtifact
	raw, err := os.ReadFile(path)
	if err != nil {
		return artifact, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return artifact, nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return artifact, nil, errors.New("artifact evidence has trailing data")
	}
	if artifact.Version != 1 || !objectID.MatchString(artifact.SourceCommit) || !objectID.MatchString(sourceTree) || artifact.SourceTree != sourceTree || artifact.BeforeTree != sourceTree || artifact.AfterTree != sourceTree {
		return artifact, nil, errors.New("desktop artifact does not bind one unchanged requested source tree")
	}
	if !filepath.IsAbs(artifact.Root) || filepath.Clean(artifact.Root) != artifact.Root || artifact.Root == "/" {
		return artifact, nil, errors.New("desktop artifact root is invalid")
	}
	if len(artifact.LibraryRoots) == 0 || len(artifact.LibraryRoots) > 32 {
		return artifact, nil, errors.New("desktop artifact requires bounded explicit library roots")
	}
	seenRoots := map[string]bool{}
	for _, root := range artifact.LibraryRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || seenRoots[root] {
			return artifact, nil, errors.New("desktop artifact library root is invalid or duplicated")
		}
		seenRoots[root] = true
	}
	resultRaw, err := checkedBytes(artifact.PreparationResult, artifact.PreparationResultSHA256)
	if err != nil {
		return artifact, nil, err
	}
	var result struct {
		ExitCode *int   `json:"exit_code"`
		State    string `json:"state"`
		JobID    string `json:"job_id"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.State != "completed" || result.JobID == "" {
		return artifact, nil, errors.New("desktop preparation did not complete successfully")
	}
	jobRaw, err := checkedBytes(artifact.PreparationJob, artifact.PreparationJobSHA256)
	if err != nil {
		return artifact, nil, err
	}
	var job struct {
		Arguments []string `json:"arguments"`
		JobID     string   `json:"job_id"`
	}
	if err := json.Unmarshal(jobRaw, &job); err != nil || job.JobID != result.JobID || len(job.Arguments) == 0 || job.Arguments[0] != "den:bundle" {
		return artifact, nil, errors.New("desktop preparation is not the declared den:bundle job")
	}
	manifestRaw, err := checkedBytes(filepath.Join(artifact.Root, "manifest.json"), artifact.ManifestSHA256)
	if err != nil {
		return artifact, nil, err
	}
	manifest, err := pkgmanifest.Read(bytes.NewReader(manifestRaw))
	if err != nil {
		return artifact, nil, err
	}
	root, err := os.OpenRoot(artifact.Root)
	if err != nil {
		return artifact, nil, err
	}
	defer root.Close()
	if err := manifest.VerifyPayload(root); err != nil {
		return artifact, nil, err
	}
	for _, name := range []string{"bin/painted-wolf-code", "bin/pw", "bin/pw-logs", "bin/pw-vault-identity", "engine-root/schemas/oar/oar.schema.json"} {
		file, err := root.Open(name)
		if err != nil {
			return artifact, nil, fmt.Errorf("actual desktop artifact lacks %s: %w", name, err)
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.Mode().IsRegular() {
			return artifact, nil, fmt.Errorf("actual desktop artifact role is not regular: %s", name)
		}
	}
	return artifact, manifest, nil
}
