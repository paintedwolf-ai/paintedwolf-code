//go:build linux

package launchguard

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// desktopEnvironmentKeys are the session facts the desktop needs to reach its
// display, buses, locale, proxies, input method and tools. The bootstrap
// executes as a privileged program for any caller, so nothing that selects
// code, libraries, modules, loaders, interpreters, runtime debug switches or
// the engine's boundary switches (LD_*, GODEBUG, GOTRACEBACK, GOMEMLIMIT,
// GOCOVERDIR, GTK_MODULES, GTK_PATH, GIO_*, GDK_PIXBUF_*, GST_*, WEBKIT_*,
// LYCAON_*, SSL_CERT_*) crosses it.
var desktopEnvironmentKeys = map[string]bool{
	"DISPLAY": true, "WAYLAND_DISPLAY": true, "XAUTHORITY": true,
	"XDG_RUNTIME_DIR": true, "XDG_SESSION_TYPE": true, "XDG_SESSION_ID": true, "XDG_SESSION_CLASS": true,
	"XDG_SESSION_DESKTOP": true, "XDG_CURRENT_DESKTOP": true, "DESKTOP_SESSION": true, "XDG_SEAT": true, "XDG_VTNR": true,
	"DBUS_SESSION_BUS_ADDRESS": true,
	"XDG_CONFIG_HOME":          true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true,
	"XDG_DATA_DIRS": true, "XDG_CONFIG_DIRS": true,
	"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "PATH": true,
	"LANG": true, "LANGUAGE": true, "TZ": true,
	"SSH_AUTH_SOCK": true, "GNUPGHOME": true, "EDITOR": true, "VISUAL": true, "BROWSER": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
	"GTK_IM_MODULE": true, "QT_IM_MODULE": true, "XMODIFIERS": true,
	"GDK_BACKEND": true, "GDK_SCALE": true, "GDK_DPI_SCALE": true, "GTK_THEME": true,
	"KDE_FULL_SESSION": true, "KDE_SESSION_VERSION": true,
}

var (
	scrubMu         sync.Mutex
	scrubbedSession []string
	scrubbedOnce    bool
)

// ScrubBootstrapEnvironment disables runtime tracebacks and clears the process
// environment before privileged work runs, returning the allowlisted session
// variables captured at entry without touching the filesystem.
func ScrubBootstrapEnvironment() []string {
	debug.SetTraceback("none")
	scrubMu.Lock()
	defer scrubMu.Unlock()
	if !scrubbedOnce {
		scrubbedSession = filterSessionEnvironment(os.Environ())
		scrubbedOnce = true
	}
	os.Clearenv()
	return append([]string(nil), scrubbedSession...)
}

// filterSessionEnvironment keeps only allowlisted session keys without
// touching caller-controlled paths on the filesystem.
func filterSessionEnvironment(environment []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] || strings.ContainsRune(entry, 0) {
			continue
		}
		if key == protectedLaunchEnv {
			continue // Only the verified entry may add the refusal-only launch marker.
		}
		if !desktopEnvironmentKeys[key] && !strings.HasPrefix(key, "LC_") {
			continue
		}
		if key == "HOME" && (!filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/") {
			continue
		}
		seen[key] = true
		out = append(out, entry)
	}
	return out
}

// DesktopEnvironment filters the caller's environment to the desktop's
// session facts. The first value of a key wins; HOME must be a directory the
// caller owns. Must be called as the unprivileged caller, never as uid 0.
func DesktopEnvironment(environment []string, uid uint32) []string {
	filtered := filterSessionEnvironment(environment)
	out := make([]string, 0, len(filtered))
	for _, entry := range filtered {
		key, value, _ := strings.Cut(entry, "=")
		if key == "HOME" && !ownedDirectory(value, uid) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func ownedDirectory(path string, uid uint32) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Uid == uid
}
