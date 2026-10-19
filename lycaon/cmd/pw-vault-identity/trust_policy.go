package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

type custodyTrustPolicy struct {
	FormatVersion  int                 `json:"format_version"`
	StoreDirectory string              `json:"store_directory"`
	Generations    []trustedGeneration `json:"generations"`
}

type trustedGeneration struct {
	InstallRoot string   `json:"install_root"`
	Manifest    string   `json:"manifest"`
	StorePaths  []string `json:"store_paths"`
}

func loadTrustPolicy(path string, base launchverify.Verifier) ([]launchverify.Verifier, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<20 {
		return nil, errors.New("custody trust policy too large")
	}
	policy, err := decodeTrustPolicy(raw)
	if err != nil {
		return nil, err
	}
	var verifiers []launchverify.Verifier
	for _, generation := range policy.Generations {
		closure, err := launchverify.NewStorePolicy(policy.StoreDirectory, generation.StorePaths)
		if err != nil {
			return nil, err
		}
		if err := closure.VerifyConfigurationFile(path); err != nil {
			return nil, err
		}
		opened, err := file.Stat()
		named, namedErr := os.Stat(path)
		if err != nil || namedErr != nil || !os.SameFile(opened, named) {
			return nil, errors.New("custody trust policy changed while opening")
		}
		if err := closure.VerifyConfigurationFile(generation.Manifest); err != nil {
			return nil, err
		}
		manifest, err := pkgmanifest.Load(generation.Manifest)
		if err != nil {
			return nil, err
		}
		if manifest.InstallRoot != generation.InstallRoot || manifest.Package != "painted-wolf-code" {
			return nil, errors.New("selected manifest does not describe its configured package generation")
		}
		verifier := base
		verifier.InstallRoot, verifier.Manifest, verifier.StorePolicy = generation.InstallRoot, manifest, closure
		verifiers = append(verifiers, verifier)
	}
	return verifiers, nil
}

func decodeTrustPolicy(raw []byte) (custodyTrustPolicy, error) {
	var policy custodyTrustPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return policy, errors.New("custody trust policy has trailing data")
	}
	if policy.FormatVersion != 1 || len(policy.Generations) == 0 || len(policy.Generations) > 8 {
		return policy, errors.New("unsupported custody trust format or generation count")
	}
	roots := map[string]struct{}{}
	for _, generation := range policy.Generations {
		output := filepath.Dir(filepath.Dir(generation.InstallRoot))
		if filepath.Join(output, "lib", "painted-wolf-code") != generation.InstallRoot || filepath.Dir(output) != policy.StoreDirectory || generation.Manifest != filepath.Join(generation.InstallRoot, "manifest.json") || !slices.Contains(generation.StorePaths, output) {
			return policy, errors.New("selected generation must name its exact private root, manifest and closure output")
		}
		if _, exists := roots[generation.InstallRoot]; exists {
			return policy, errors.New("duplicate selected generation")
		}
		roots[generation.InstallRoot] = struct{}{}
		if _, err := launchverify.NewStorePolicy(policy.StoreDirectory, generation.StorePaths); err != nil {
			return policy, fmt.Errorf("selected generation: %w", err)
		}
	}
	return policy, nil
}
