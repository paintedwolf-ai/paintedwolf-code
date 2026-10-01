package logview

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// Capture is a resolved debug capture and the standard files it exposes.
// Each path prefers the named session directory, then wherever the writer is
// writing: a LYCAON_*_FILE redirect, else the run's session directory. The
// layout is [debugpaths]. A file the run never enabled loads as an empty
// slice with a nil error.
type Capture struct {
	Dir          string
	LLMPath      string
	HTTPPath     string
	SSEPath      string
	SessionsPath string
	LogPath      string
	DenPerfPath  string
	ToolPath     string
}

// Resolve locates a capture. Precedence: an explicit dir, then the run's
// session dir, then debug/latest, then a LYCAON_*_FILE redirect target outside
// the tree. A non-path dir argument is treated as a session folder name under
// debug/sessions/.
func Resolve(dir string) (*Capture, error) {
	resolved, err := resolveDir(dir)
	if err != nil {
		return nil, err
	}
	return newCapture(resolved), nil
}

func newCapture(dir string) *Capture {
	return &Capture{
		Dir:          dir,
		LLMPath:      preferCaptureFile(dir, debugpaths.KindLLM),
		HTTPPath:     preferCaptureFile(dir, debugpaths.KindHTTP),
		SSEPath:      preferCaptureFile(dir, debugpaths.KindSSE),
		SessionsPath: preferCaptureFile(dir, debugpaths.KindSessions),
		LogPath:      preferCaptureFile(dir, debugpaths.KindSidecarLog),
		DenPerfPath:  preferCaptureFile(dir, debugpaths.KindDenPerf),
		ToolPath:     preferCaptureFile(dir, debugpaths.KindTool),
	}
}

// preferCaptureFile selects the session capture, then the active writer path.
func preferCaptureFile(sessionDir string, kind debugpaths.Kind) string {
	written := debugpaths.FilePath(kind)
	if sessionDir == "" {
		return written
	}
	sessionPath := filepath.Join(sessionDir, debugpaths.Name(kind))
	if fileExists(sessionPath) {
		return sessionPath
	}
	if written != "" && fileExists(written) {
		return written
	}
	return sessionPath
}

func fileExists(p string) bool {
	info, err := os.Stat(p) // #nosec G703 -- local debug CLI path resolution
	return err == nil && !info.IsDir()
}

// redirectCaptureDir is the directory of a written LYCAON_*_FILE redirect
// target outside the debug tree, the last resort in resolution. Empty when no
// redirect is set or its target is unwritten.
func redirectCaptureDir() string {
	sessions := debugpaths.SessionsRoot()
	for _, f := range debugpaths.Files() {
		p := debugpaths.Override(f.Kind)
		if p == "" || !fileExists(p) {
			continue
		}
		dir := filepath.Dir(p)
		if rel, err := filepath.Rel(sessions, dir); err == nil && !strings.HasPrefix(rel, "..") {
			continue
		}
		return dir
	}
	return ""
}

// NewestCapture resolves the most recent capture to its real directory (following
// the `latest` symlink, falling back to the newest-named session folder, then a
// redirect target). Follow mode polls this so it tracks the live sidecar.
func NewestCapture() (*Capture, error) {
	if real, err := filepath.EvalSymlinks(debugpaths.LatestLink()); err == nil && isDir(real) {
		return newCapture(real), nil
	}
	dirs, err := SessionDirs()
	if err == nil && len(dirs) > 0 {
		return newCapture(debugpaths.SessionDir(dirs[0])), nil
	}
	if dir := redirectCaptureDir(); dir != "" {
		return newCapture(dir), nil
	}
	return nil, fmt.Errorf("no captures found")
}

func resolveDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = debugpaths.ActiveSessionDir()
	}

	if dir != "" {
		if isDir(dir) {
			return dir, nil
		}
		// Bare name (e.g. a timestamp) resolves under the sessions folder.
		if cand := debugpaths.SessionDir(dir); isDir(cand) {
			return cand, nil
		}
		return "", fmt.Errorf("no capture directory at %q", dir)
	}

	latest := debugpaths.LatestLink()
	if isDir(latest) {
		return latest, nil
	}
	if redirect := redirectCaptureDir(); redirect != "" {
		return redirect, nil
	}
	return "", fmt.Errorf("no capture found — run `./task den:sidecar:full-debug` or enable Settings → Full debug logging (looked for %s)", latest)
}

func isDir(p string) bool {
	info, err := os.Stat(p) // #nosec G703 -- local debug CLI: operator names the capture dir to inspect
	return err == nil && info.IsDir()
}

// SessionDirs lists captured session folders newest-first, for `logs sessions --list`.
func SessionDirs() ([]string, error) {
	entries, err := os.ReadDir(debugpaths.SessionsRoot())
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// LLM loads the captured provider calls. A missing file yields nil records.
func (c *Capture) LLM() ([]LLMRecord, error)   { return loadOrEmpty[LLMRecord](c.LLMPath) }
func (c *Capture) HTTP() ([]HTTPRecord, error) { return loadOrEmpty[HTTPRecord](c.HTTPPath) }
func (c *Capture) SSE() ([]SSERecord, error)   { return loadOrEmpty[SSERecord](c.SSEPath) }
func (c *Capture) Sessions() ([]SessionRecord, error) {
	return loadOrEmpty[SessionRecord](c.SessionsPath)
}

// DenPerf loads Den main-thread stall/perf lines (den-perf.jsonl).
func (c *Capture) DenPerf() ([]DenPerfRecord, error) {
	return loadOrEmpty[DenPerfRecord](c.DenPerfPath)
}

func loadOrEmpty[T any](path string) ([]T, error) {
	if path == "" {
		return nil, nil
	}
	recs, err := decodeJSONL[T](path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return recs, err
}
