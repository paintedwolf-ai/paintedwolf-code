//go:build linux

package launchguard

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// desktopEnvironmentKeys are the session facts the desktop needs to reach its
// display, buses, locale, proxies, input method and tools. The bootstrap
// executes as a privileged program for any caller, so nothing that selects
// code, libraries, modules, loaders, interpreters or the engine's boundary
// switches (LD_*, GTK_MODULES, GTK_PATH, GIO_*, GDK_PIXBUF_*, GST_*,
// WEBKIT_*, LYCAON_*, SSL_CERT_*) crosses it.
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

// DesktopEnvironment filters the caller's environment to the desktop's
// session facts. The first value of a key wins; HOME must be a directory the
// caller owns.
func DesktopEnvironment(environment []string, uid uint32) []string {
	seen := map[string]bool{}
	var out []string
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] || strings.ContainsRune(entry, 0) {
			continue
		}
		if !desktopEnvironmentKeys[key] && !strings.HasPrefix(key, "LC_") {
			continue
		}
		if key == "HOME" && !ownedDirectory(value, uid) {
			continue
		}
		seen[key] = true
		out = append(out, entry)
	}
	return out
}

func ownedDirectory(path string, uid uint32) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	facts, ok := info.Sys().(*syscall.Stat_t)
	return ok && facts.Uid == uid
}
