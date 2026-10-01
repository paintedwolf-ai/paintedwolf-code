package confine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
)

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
