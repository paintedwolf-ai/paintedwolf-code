package confine

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// sshKnownHostsPath names the single writable key file for this mediated project.
func sshKnownHostsPath(c Confinement) (string, error) {
	if c.Network != NetworkProxyOnly || c.ProjectID == "" {
		return "", nil
	}
	stateRoot, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(c.ProjectID))
	return filepath.Join(fspath.CanonicalPath(enginepaths.SSHRootUnder(stateRoot)), "projects", fmt.Sprintf("%x", id), "known_hosts"), nil
}

// OpenSSH parses each file name after the shell has split its arguments.
func sshConfigPath(path string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(path) + `"`
}

// PrepareSSHKnownHosts creates the file before Linux binds it into the namespace.
func PrepareSSHKnownHosts(c Confinement) (string, error) {
	path, err := sshKnownHostsPath(c)
	if err != nil || path == "" {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.OpenFile("known_hosts", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := dir.Lstat("known_hosts")
		if statErr != nil {
			return "", statErr
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("expected regular SSH host key file at %s", path)
		}
		return path, nil
	}
	if err != nil {
		return "", err
	}
	return path, f.Close()
}

func addSSHKnownHostsAccess(rules *FilesystemRules, c Confinement) error {
	if c.privateUserland {
		return nil
	}
	state, err := configdir.Root()
	if err != nil {
		return err
	}
	mirror := fspath.CanonicalPath(enginepaths.SSHKnownHostsMirrorUnder(state))
	readable := []string{mirror}
	path, err := sshKnownHostsPath(c)
	if err != nil {
		return err
	}
	if path != "" {
		readable = append(readable, path)
		rules.add(fsRule{op: opWrite, allow: true, matches: literalMatches([]string{path}, "")})
	}
	rules.add(fsRule{op: opRead, allow: true, matches: literalMatches(readable, "")})
	parents := traversalAncestors(controlPlaneReadDenyRootsResolved(), readable)
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(parents, "")})
	return nil
}

// userKnownHostsSources are user SSH host-key stores.
var userKnownHostsSources = []string{
	".ssh/known_hosts",
	".ssh/known_hosts2",
}

// systemKnownHostsSources are system SSH host-key stores.
var systemKnownHostsSources = []string{
	"/etc/ssh/ssh_known_hosts",
	"/etc/ssh/ssh_known_hosts2",
}

var (
	knownHostsMirrorOnce sync.Once
	knownHostsMirrorPath string
)

// hostKnownHostsMirror returns read-only host verification material.
func hostKnownHostsMirror() string {
	knownHostsMirrorOnce.Do(func() {
		knownHostsMirrorPath = buildKnownHostsMirror()
	})
	return knownHostsMirrorPath
}

func buildKnownHostsMirror() string {
	stateRoot, err := configdir.UserConfigDir()
	if err != nil || strings.TrimSpace(stateRoot) == "" {
		return ""
	}
	seed := collectKnownHosts()
	if len(seed) == 0 {
		// An empty mirror adds no verification signal.
		return ""
	}
	path := enginepaths.SSHKnownHostsMirrorUnder(stateRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(seed),
		Mode:     0o600,
		DirMode:  0o700,
	}); err != nil {
		return ""
	}
	return path
}

// collectKnownHosts separates concatenated entries with newlines.
func collectKnownHosts() []byte {
	var buf bytes.Buffer
	appendFile := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			return
		}
		buf.Write(data)
		if data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, rel := range userKnownHostsSources {
			appendFile(filepath.Join(home, rel))
		}
	}
	for _, path := range systemKnownHostsSources {
		appendFile(path)
	}
	return buf.Bytes()
}
