package main

import (
	"fmt"
	"os"
	"path/filepath"
)

type fixtureShape struct {
	Directories int   `json:"directories"`
	Files       int   `json:"files"`
	Bytes       int64 `json:"bytes"`
}

func buildFixture(root, scale string) (fixtureShape, error) {
	dirs, filesPerDir, fileBytes := 20, 50, 1024
	switch scale {
	case "small":
		dirs, filesPerDir, fileBytes = 5, 20, 512
	case "medium":
	case "large":
		dirs, filesPerDir, fileBytes = 100, 100, 2048
	default:
		return fixtureShape{}, fmt.Errorf("unknown scale %q", scale)
	}
	body := make([]byte, fileBytes)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	for dirIndex := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("pkg%04d", dirIndex))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fixtureShape{}, err
		}
		for fileIndex := range filesPerDir {
			path := filepath.Join(dir, fmt.Sprintf("file%04d.go", fileIndex))
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return fixtureShape{}, err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# performance fixture\n"), 0o600); err != nil {
		return fixtureShape{}, err
	}
	files := dirs*filesPerDir + 1
	return fixtureShape{Directories: dirs, Files: files, Bytes: int64(dirs*filesPerDir*fileBytes + len("# performance fixture\n"))}, nil
}
