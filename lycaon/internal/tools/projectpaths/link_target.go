package projectpaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// AdmitLinkTarget checks the location a stored link text would reach. Missing
// descendants are allowed; every existing link in the path is resolved first.
func AdmitLinkTarget(tctx tools.ToolContext, linkAbs, target string) error {
	roots := make([]string, 0, len(tctx.Source.Roots)+1)
	for _, root := range tctx.Source.Roots {
		roots = append(roots, root.Path)
	}
	if tctx.Host.SessionScratchDir != "" {
		roots = append(roots, tctx.Host.SessionScratchDir)
	}
	return AdmitLinkTargetRoots(linkAbs, target, roots)
}

// AdmitLinkTargetRoots applies the same confinement rule to retained links
// restored by source history, where no tool context remains.
func AdmitLinkTargetRoots(linkAbs, target string, roots []string) error {
	if target == "" {
		return fmt.Errorf("symbolic link target is empty")
	}
	address := target
	if !filepath.IsAbs(address) {
		address = filepath.Dir(linkAbs) + string(filepath.Separator) + address
	}
	resolved, err := prospectiveLinkTarget(address)
	if err != nil {
		return err
	}
	for _, root := range roots {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(canonical, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return &PathError{Code: "SURVEY_PATH_ESCAPE", Operand: target, Cause: errors.New("symbolic link target is outside attached roots and session scratch")}
}

func prospectiveLinkTarget(address string) (string, error) {
	if !filepath.IsAbs(address) {
		return "", fmt.Errorf("symbolic link target must resolve to an absolute path")
	}
	volume := filepath.VolumeName(address)
	root := volume + string(filepath.Separator)
	remaining := strings.Split(strings.TrimPrefix(address[len(volume):], string(filepath.Separator)), string(filepath.Separator))
	current := root
	links := 0
	for len(remaining) > 0 {
		part := remaining[0]
		remaining = remaining[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			current = filepath.Dir(current)
			continue
		}
		candidate := filepath.Join(current, part)
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Join(append([]string{candidate}, remaining...)...), nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = candidate
			continue
		}
		links++
		if links > 40 {
			return "", fmt.Errorf("symbolic link target has too many links")
		}
		text, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(text) {
			volume = filepath.VolumeName(text)
			current = volume + string(filepath.Separator)
			text = strings.TrimPrefix(text[len(volume):], string(filepath.Separator))
		}
		remaining = append(strings.Split(text, string(filepath.Separator)), remaining...)
	}
	return current, nil
}
