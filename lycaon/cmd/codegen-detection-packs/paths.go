package main

import (
	"path/filepath"
)

type modulePaths struct {
	root               string
	structuredCloudMap string
}

func resolveModulePaths(root string) (modulePaths, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return modulePaths{}, err
	}
	upstream := filepath.Join(abs, "config", "packs", "painted-wolf", "security", "host", "detection-pack-upstream")
	return modulePaths{
		root:               abs,
		structuredCloudMap: filepath.Join(upstream, "structured-cloud-actions.yaml"),
	}, nil
}
