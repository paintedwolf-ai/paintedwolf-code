// Package debugpaths defines the shared debug capture layout, catalog, and
// switches.
//
// Layout under Root:
//
//	<root>/debug/sessions/<stamp>/    one capture session: every enabled file of one engine run
//	<root>/debug/latest               symlink to the newest session dir
//
// A run with any capture enabled writes into one stamped session directory,
// minted by a dev script or by the engine at boot. A LYCAON_*_FILE redirect
// wins for its own file.
package debugpaths

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
)

// Kind identifies one capture file in the catalog.
type Kind string

const (
	KindLLM         Kind = "llm"
	KindSessions    Kind = "sessions"
	KindHTTP        Kind = "http"
	KindSSE         Kind = "sse"
	KindTool        Kind = "tool"
	KindDenPerf     Kind = "den_perf"
	KindWebSearch   Kind = "web_search"
	KindSummarize   Kind = "summarize"
	KindPromptCache Kind = "prompt_cache"
	KindPerformance Kind = "performance"
	KindSidecarLog  Kind = "sidecar_log"
)

// Switches shared by the engine, the desktop shell, and the dev scripts.
const (
	// FullDebugEnv is the main switch: it turns on every capture in the
	// catalog plus the debug slog level. Settings → Full debug logging sets it
	// on sidecar spawn, so a new capture is covered without editing the shell.
	FullDebugEnv = "LYCAON_DEBUG_ALL"
	// SessionDirEnv names the capture directory of this run. The dev scripts
	// export it before launch; an engine that finds it unset mints its own.
	SessionDirEnv = "LYCAON_DEBUG_SESSION_DIR"
)

// File is one capture file: what it is called, what turns it on, and how its
// location moves.
type File struct {
	Kind Kind
	// Name is the basename inside the session dir.
	Name string
	// EnableEnv turns this capture on by itself. Empty when the file has no
	// switch of its own and follows a sibling, or is on only under FullDebugEnv.
	EnableEnv string
	// FileEnv redirects this file wholesale. Empty when the file has no variable
	// of its own and follows a sibling instead.
	FileEnv string
	// SiblingOf names the capture whose switch and directory this file follows:
	// the session manifest and the prompt-cache rows are written next to
	// llm-requests.jsonl, wherever that landed.
	SiblingOf Kind
}

// Layout segments under the config root.
const (
	debugDirName    = "debug"
	sessionsDirName = "sessions"
	latestLinkName  = "latest"

	// sessionStampLayout names a session dir by the UTC second it started.
	sessionStampLayout = "20060102T150405Z"

	// tempFallbackDir holds captures when the config root cannot be resolved or
	// created. Writer and reader share it so they still agree in that mode.
	tempFallbackDir = "lycaon-debug"
)

// catalog is the closed set of capture files, in listing order.
var catalog = []File{
	{Kind: KindLLM, Name: "llm-requests.jsonl", EnableEnv: "LYCAON_LLM_DEBUG", FileEnv: "LYCAON_LLM_DEBUG_FILE"},
	{Kind: KindSessions, Name: "sessions.jsonl", SiblingOf: KindLLM},
	{Kind: KindHTTP, Name: "http-requests.jsonl", EnableEnv: "LYCAON_HTTP_DEBUG", FileEnv: "LYCAON_HTTP_DEBUG_FILE"},
	{Kind: KindSSE, Name: "sse-events.jsonl", EnableEnv: "LYCAON_SSE_DEBUG", FileEnv: "LYCAON_SSE_DEBUG_FILE"},
	{Kind: KindTool, Name: "tool-invocations.jsonl", EnableEnv: "LYCAON_TOOL_DEBUG", FileEnv: "LYCAON_TOOL_DEBUG_FILE"},
	{Kind: KindDenPerf, Name: "den-perf.jsonl", EnableEnv: "LYCAON_DEN_PERF_DEBUG", FileEnv: "LYCAON_DEN_PERF_DEBUG_FILE"},
	{Kind: KindWebSearch, Name: "web-search.jsonl", EnableEnv: "LYCAON_WEBSEARCH_DEBUG", FileEnv: "LYCAON_WEBSEARCH_DEBUG_FILE"},
	{Kind: KindSummarize, Name: "summarize-debug.jsonl", EnableEnv: "LYCAON_SUMMARIZE_DEBUG", FileEnv: "LYCAON_SUMMARIZE_DEBUG_FILE"},
	{Kind: KindPromptCache, Name: "prompt-cache-observability.jsonl", SiblingOf: KindLLM},
	{Kind: KindPerformance, Name: "performance.jsonl", EnableEnv: "LYCAON_PERF_DEBUG", FileEnv: "LYCAON_PERF_DEBUG_FILE"},
	// The sidecar slog has no switch of its own: it reaches a file under full
	// debug logging, or wherever LYCAON_LOG_FILE points.
	{Kind: KindSidecarLog, Name: "sidecar.log", FileEnv: "LYCAON_LOG_FILE"},
}

// Files returns a copy of the catalog in listing order.
func Files() []File {
	out := make([]File, len(catalog))
	copy(out, catalog)
	return out
}

// Entry returns the catalog entry for kind.
func Entry(k Kind) (File, bool) {
	for _, f := range catalog {
		if f.Kind == k {
			return f, true
		}
	}
	return File{}, false
}

// Name returns the basename for kind, or empty for an unknown kind.
func Name(k Kind) string {
	f, _ := Entry(k)
	return f.Name
}

// FullDebugEnabled reports whether the main switch is set.
func FullDebugEnabled() bool {
	return configdir.EnvTruthy(os.Getenv(FullDebugEnv))
}

// Enabled reports whether kind is captured this run: the main switch, the
// capture's own switch, or the switch of the sibling it follows. A file with
// neither switch is on only under the main switch.
func Enabled(k Kind) bool {
	f, ok := Entry(k)
	if !ok {
		return false
	}
	if FullDebugEnabled() {
		return true
	}
	if f.EnableEnv != "" && configdir.EnvTruthy(os.Getenv(f.EnableEnv)) {
		return true
	}
	if f.SiblingOf != "" {
		return Enabled(f.SiblingOf)
	}
	return false
}

// AnyEnabled reports whether this run writes any capture at all.
func AnyEnabled() bool {
	for _, f := range catalog {
		if Enabled(f.Kind) || Override(f.Kind) != "" {
			return true
		}
	}
	return false
}

// Root is the config/data root holding the debug tree ([configdir.UserConfigDir]).
// When that cannot be resolved or created — a sandboxed test, an unwritable HOME —
// it degrades to a fixed directory under the system temp dir rather than guessing
// a second home-relative location.
func Root() string {
	if dir, err := configdir.UserConfigDir(); err == nil && dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), tempFallbackDir)
}

// DebugRootUnder is the per-session capture tree under an explicit base.
func DebugRootUnder(base string) string { return filepath.Join(base, debugDirName) }

// DebugRoot is the per-session capture tree under [Root].
func DebugRoot() string { return DebugRootUnder(Root()) }

// SessionsRoot holds one directory per captured session.
func SessionsRoot() string { return filepath.Join(DebugRoot(), sessionsDirName) }

// SessionDir is the capture directory for a session name (a UTC timestamp).
func SessionDir(name string) string { return filepath.Join(SessionsRoot(), name) }

// LatestLink is the symlink that points at the newest session dir.
func LatestLink() string { return filepath.Join(DebugRoot(), latestLinkName) }

// LatestLabel is the display form of that symlink (~/.config/<leaf>/debug/latest)
// for help text, never a resolved absolute path. Mirrors [configdir.Label].
func LatestLabel() string {
	return path.Join(configdir.Label(), debugDirName, latestLinkName)
}

// SessionStamp names a session dir for the moment it started.
func SessionStamp(at time.Time) string { return at.UTC().Format(sessionStampLayout) }

// ParseSessionStamp decodes a session dir name back into its start time. A
// name minted under contention carries a suffix after the stamp.
func ParseSessionStamp(name string) (time.Time, error) {
	if len(name) < len(sessionStampLayout) {
		return time.Time{}, fmt.Errorf("session name %q is shorter than a stamp", name)
	}
	return time.Parse(sessionStampLayout, name[:len(sessionStampLayout)])
}

// ActiveSessionDir is the capture directory this run writes into, or empty
// when none has been established.
func ActiveSessionDir() string { return strings.TrimSpace(os.Getenv(SessionDirEnv)) }

// EnsureSessionDir establishes the run's capture directory when any capture
// is enabled and none is set yet: it mints a stamped directory, points
// `latest` at it, and publishes it through SessionDirEnv so child processes
// and later lookups agree. It returns the directory, or empty when this run
// captures nothing.
func EnsureSessionDir() (string, error) {
	if dir := ActiveSessionDir(); dir != "" {
		return dir, nil
	}
	if !AnyEnabled() {
		return "", nil
	}
	dir, err := mintSessionDir(time.Now())
	if err != nil {
		return "", err
	}
	if err := os.Setenv(SessionDirEnv, dir); err != nil {
		return "", fmt.Errorf("publish debug session dir: %w", err)
	}
	pointLatest(dir)
	return dir, nil
}

// mintSessionDir creates a fresh session directory. Two engines started in
// the same second on one config root get distinct directories.
func mintSessionDir(at time.Time) (string, error) {
	stamp := SessionStamp(at)
	for _, name := range []string{stamp, stamp + "-" + strconv.Itoa(os.Getpid())} {
		dir := SessionDir(name)
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return "", fmt.Errorf("create debug sessions root: %w", err)
		}
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create debug session dir: %w", err)
		}
	}
	return "", fmt.Errorf("debug session dir for %s already exists", stamp)
}

// pointLatest retargets the `latest` symlink; a link that cannot be written
// only costs the viewer its shortcut.
func pointLatest(dir string) {
	link := LatestLink()
	_ = os.Remove(link)
	_ = os.Symlink(dir, link)
}

// Override returns the path kind was redirected to by its LYCAON_*_FILE
// variable, or empty when unset.
func Override(k Kind) string {
	f, ok := Entry(k)
	if !ok || f.FileEnv == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(f.FileEnv))
}

// FilePath resolves where kind is written and read: its LYCAON_*_FILE override
// when set, then the directory of the sibling it follows, otherwise [Name]
// inside the run's session directory. Empty when this run has no session
// directory and no redirect for the kind.
func FilePath(k Kind) string {
	f, ok := Entry(k)
	if !ok {
		return ""
	}
	if p := Override(k); p != "" {
		return p
	}
	if f.SiblingOf != "" {
		if sibling := FilePath(f.SiblingOf); sibling != "" {
			return filepath.Join(filepath.Dir(sibling), f.Name)
		}
	}
	if dir := ActiveSessionDir(); dir != "" {
		return filepath.Join(dir, f.Name)
	}
	return ""
}
