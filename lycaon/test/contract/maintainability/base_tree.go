package maintainability

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os/exec"
)

// openBaseTree measures the actual base source with the current measurement rules.
func openBaseTree(root, base string) (*workingTree, error) {
	raw, err := exec.Command("git", "-C", root, "archive", base).Output()
	if err != nil {
		return nil, fmt.Errorf("archive budget base: %w", err)
	}
	tree := &workingTree{root: root, bodies: map[string][]byte{}}
	archive := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read budget base: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		tree.paths = append(tree.paths, header.Name)
		body, err := io.ReadAll(archive)
		if err != nil {
			return nil, fmt.Errorf("read budget base file: %w", err)
		}
		tree.bodies[header.Name] = body
	}
	return tree, nil
}
