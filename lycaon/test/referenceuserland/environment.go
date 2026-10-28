package referenceuserland

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// SeedTime is the access and modification time of every seeded workspace entry.
var SeedTime = time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

const (
	referenceUser = "reference"
	referenceUID  = 1000
	referenceGID  = 1000
	referenceHost = "reference"
	workspaceDir  = "/workspace"
	homeDir       = "/home/reference"
	tmpDir        = "/tmp"
	// searchPath includes the sbin directories, where FreeBSD keeps chown.
	searchPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	locale     = "C.UTF-8"
)

// Environment is the complete environment of a program run by shell.
func Environment(shell string) []string {
	return environment("/", shell)
}

func environment(root, shell string) []string {
	return []string{
		"HOME=" + filepath.Join(root, homeDir),
		"LANG=" + locale,
		"LC_ALL=" + locale,
		"LOGNAME=" + referenceUser,
		"PATH=" + searchPath,
		"PWD=" + filepath.Join(root, workspaceDir),
		"SHELL=" + shell,
		"TMPDIR=" + filepath.Join(root, tmpDir),
		"TZ=UTC",
		"USER=" + referenceUser,
	}
}

// Entry is a workspace file, directory or symbolic link. Mode carries the type and
// permission bits; a seeded entry with no permission bits gets 0644, or 0755 for a
// directory.
type Entry struct {
	Mode fs.FileMode `json:"mode"`
	Data []byte      `json:"data,omitempty"`
	Link string      `json:"link,omitempty"`
}

// Case is one program and the workspace it starts from.
type Case struct {
	Program   string           `json:"program"`
	Workspace map[string]Entry `json:"workspace,omitempty"`
	// Tracked names the workspace paths whose final state the result reports.
	Tracked []string `json:"tracked,omitempty"`
}

// Result is what a program left: its status, its output streams, and the final
// state of each tracked path, nil when the path does not exist.
type Result struct {
	Status int               `json:"status"`
	Stdout []byte            `json:"stdout"`
	Stderr []byte            `json:"stderr"`
	Files  map[string]*Entry `json:"files"`
}

// Inventory describes an image's userland.
type Inventory struct {
	Architecture string `json:"architecture"`
	// Tree is the digest of every file's path, type, permissions, owner and content.
	Tree    string `json:"tree"`
	Entries int    `json:"entries"`
	// Commands are the executable names the fixed PATH finds.
	Commands []string `json:"commands"`
}

func (c Case) validate() error {
	for name, entry := range c.Workspace {
		if err := workspacePath(name); err != nil {
			return err
		}
		if entry.Link != "" && (entry.Mode.Type() != fs.ModeSymlink || len(entry.Data) > 0) {
			return fmt.Errorf("workspace entry %q: a link carries only its target", name)
		}
		if entry.Mode.Type() == fs.ModeSymlink && entry.Link == "" {
			return fmt.Errorf("workspace entry %q: a symbolic link needs a target", name)
		}
		if entry.Mode.IsDir() && len(entry.Data) > 0 {
			return fmt.Errorf("workspace entry %q: a directory has no data", name)
		}
		if t := entry.Mode.Type(); t != 0 && t != fs.ModeDir && t != fs.ModeSymlink {
			return fmt.Errorf("workspace entry %q: only files, directories and symbolic links are seeded", name)
		}
	}
	for _, name := range c.Tracked {
		if err := workspacePath(name); err != nil {
			return err
		}
	}
	return nil
}

func workspacePath(name string) error {
	if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("workspace path %q is not a clean relative path inside the workspace", name)
	}
	return nil
}
