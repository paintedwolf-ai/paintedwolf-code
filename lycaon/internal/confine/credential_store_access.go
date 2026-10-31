package confine

import (
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

var darwinLoginKeychainFiles = []string{
	"login.keychain-db",
	"login.keychain",
}

var linuxCredentialStoreRelDirs = []string{
	filepath.Join(".local", "share", "keyrings"),
	filepath.Join(".local", "share", "kwalletd"),
}

// darwinLoginKeychainLiterals returns the exact login keychain database paths
// admitted read-only when CredentialStore is granted.
func darwinLoginKeychainLiterals() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	lexicalDir := filepath.Clean(filepath.Join(home, "Library", "Keychains"))
	canonicalDir := fspath.CanonicalPath(lexicalDir)
	if canonicalDir == "" {
		canonicalDir = lexicalDir
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, name := range darwinLoginKeychainFiles {
		add(filepath.Join(canonicalDir, name))
		if resolved := fspath.CanonicalPath(filepath.Join(canonicalDir, name)); resolved != "" && PathAtOrUnder(resolved, canonicalDir) {
			add(resolved)
		}
		if lexicalDir != canonicalDir {
			add(filepath.Join(lexicalDir, name))
		}
	}
	sort.Strings(out)
	return out
}

// darwinLoginKeychainMetadataDirs returns the Keychains directory paths that
// require file-read-metadata traversal when CredentialStore is granted.
func darwinLoginKeychainMetadataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	lexicalDir := filepath.Clean(filepath.Join(home, "Library", "Keychains"))
	canonicalDir := fspath.CanonicalPath(lexicalDir)
	if canonicalDir == "" || canonicalDir == lexicalDir {
		return []string{lexicalDir}
	}
	out := []string{canonicalDir, lexicalDir}
	sort.Strings(out)
	return out
}

// linuxCredentialStoreReadRoots returns the Secret Service and KWallet store
// directories admitted read-only when CredentialStore is granted.
func linuxCredentialStoreReadRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	shareLexical := filepath.Clean(filepath.Join(home, ".local", "share"))
	shareCanonical := fspath.CanonicalPath(shareLexical)
	if shareCanonical == "" {
		shareCanonical = shareLexical
	}
	controlRoots := resolveEach(controlPlaneReadDenyRoots())
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		for _, root := range controlRoots {
			if PathAtOrUnder(p, root) {
				return
			}
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, rel := range linuxCredentialStoreRelDirs {
		base := filepath.Base(rel)
		expectedCanonical := filepath.Join(shareCanonical, base)
		lexical := filepath.Clean(filepath.Join(home, rel))
		resolved := fspath.CanonicalPath(lexical)
		if resolved == "" {
			resolved = expectedCanonical
		}
		// Reject symlinks that redirect the store outside its expected location.
		if !PathAtOrUnder(resolved, expectedCanonical) {
			continue
		}
		add(resolved)
		if lexical != resolved {
			add(lexical)
		}
	}
	sort.Strings(out)
	return out
}

// ResolveCredentialStoreDBusSocket resolves the host session D-Bus AF_UNIX
// socket path admitted on Linux when CredentialStore is granted.
func ResolveCredentialStoreDBusSocket(lookupEnv func(string) (string, bool), uid int) (string, bool) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if address, ok := lookupEnv("DBUS_SESSION_BUS_ADDRESS"); ok && strings.TrimSpace(address) != "" {
		candidates, ok := parseDBusUnixSocketCandidates(strings.TrimSpace(address))
		if !ok {
			return "", false
		}
		for _, candidate := range candidates {
			if resolved, valid := validateCredentialStoreDBusSocket(candidate, uid); valid {
				return resolved, true
			}
		}
		return "", false
	}
	if runtimeDir, ok := lookupEnv("XDG_RUNTIME_DIR"); ok && strings.TrimSpace(runtimeDir) != "" {
		runtimeDir = strings.TrimSpace(runtimeDir)
		if !filepath.IsAbs(runtimeDir) || strings.ContainsAny(runtimeDir, "\x00\r\n") {
			return "", false
		}
		return validateCredentialStoreDBusSocket(filepath.Join(runtimeDir, "bus"), uid)
	}
	if uid < 0 {
		return "", false
	}
	return validateCredentialStoreDBusSocket(filepath.Join("/run/user", strconv.Itoa(uid), "bus"), uid)
}

func credentialStoreDBusSocketPaths() []string {
	resolved, ok := ResolveCredentialStoreDBusSocket(os.LookupEnv, os.Getuid())
	if !ok || resolved == "" {
		return nil
	}
	return []string{resolved}
}

func parseDBusUnixSocketCandidates(address string) ([]string, bool) {
	if len(address) > 64<<10 {
		return nil, false
	}
	alternatives := strings.Split(address, ";")
	if len(alternatives) > 16 {
		return nil, false
	}
	var out []string
	for _, alt := range alternatives {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		path, ok := parseSingleDBusUnixPath(alt)
		if !ok {
			continue
		}
		out = append(out, path)
	}
	return out, len(out) > 0
}

func parseSingleDBusUnixPath(address string) (string, bool) {
	rest, ok := strings.CutPrefix(address, "unix:")
	if !ok || rest == "" {
		return "", false
	}
	values := map[string]string{}
	for _, field := range strings.Split(rest, ",") {
		key, encoded, ok := strings.Cut(field, "=")
		if !ok || values[key] != "" {
			return "", false
		}
		switch key {
		case "path", "guid":
		default:
			return "", false
		}
		val, ok := unescapeDBusValue(encoded)
		if !ok || val == "" || strings.ContainsAny(val, "\x00\r\n") {
			return "", false
		}
		values[key] = val
	}
	if guid, hasGUID := values["guid"]; hasGUID {
		if decoded, err := hex.DecodeString(guid); err != nil || len(decoded) != 16 {
			return "", false
		}
	}
	path := values["path"]
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

func unescapeDBusValue(encoded string) (string, bool) {
	for i := 0; i < len(encoded); i++ {
		b := encoded[i]
		if b == '%' {
			if i+2 >= len(encoded) {
				return "", false
			}
			i += 2
			continue
		}
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.ContainsRune("-_/.*", rune(b))) {
			return "", false
		}
	}
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		return "", false
	}
	return decoded, true
}

func validateCredentialStoreDBusSocket(candidate string, uid int) (string, bool) {
	if socketPathSyntaxReason(candidate) != "" {
		return "", false
	}
	cleanCandidate := filepath.Clean(candidate)
	resolved := fspath.CanonicalPath(cleanCandidate)
	if resolved == "" || socketPathSyntaxReason(resolved) != "" {
		return "", false
	}
	checkPaths := []string{cleanCandidate, resolved}
	if linkTarget, err := os.Readlink(cleanCandidate); err == nil && linkTarget != "" {
		if !filepath.IsAbs(linkTarget) {
			linkTarget = filepath.Join(filepath.Dir(cleanCandidate), linkTarget)
		}
		checkPaths = append(checkPaths, filepath.Clean(linkTarget))
	}
	for _, root := range resolveEach(controlPlaneReadDenyRoots()) {
		for _, p := range checkPaths {
			if PathAtOrUnder(p, root) {
				return "", false
			}
		}
	}
	for _, root := range KeyMaterialReadDenyPaths() {
		for _, p := range checkPaths {
			if PathAtOrUnder(p, root) {
				return "", false
			}
		}
	}
	for _, systemBusRoot := range []string{"/run/dbus", "/var/run/dbus", "/private/var/run/dbus"} {
		for _, p := range checkPaths {
			if PathAtOrUnder(p, systemBusRoot) {
				return "", false
			}
		}
	}
	if !underAllowedDBusRuntimeRoot(resolved, uid) {
		return "", false
	}
	fi, err := os.Lstat(resolved)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return "", false
	}
	return resolved, true
}

func underAllowedDBusRuntimeRoot(resolved string, uid int) bool {
	var allowed []string
	if uid >= 0 {
		u := strconv.Itoa(uid)
		allowed = append(allowed,
			filepath.Join("/run/user", u),
			filepath.Join("/var/run/user", u),
			filepath.Join("/private/var/run/user", u),
		)
	}
	allowed = append(allowed, "/tmp", "/private/tmp")
	if tmp := strings.TrimSpace(os.TempDir()); filepath.IsAbs(tmp) {
		allowed = append(allowed, filepath.Clean(tmp))
		if canonTmp := fspath.CanonicalPath(tmp); canonTmp != "" {
			allowed = append(allowed, canonTmp)
		}
	}
	for _, root := range allowed {
		if PathAtOrUnder(resolved, root) {
			return true
		}
	}
	return false
}

func mergeSortedStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, item := range list {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
