//go:build linux

package linuxbus

import (
	"context"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// ConnectSession uses the inherited session address without launching a bus.
func ConnectSession(ctx context.Context) (*dbus.Conn, error) {
	paths, err := sessionSocketPaths(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), os.Getenv("XDG_RUNTIME_DIR"), os.Getuid())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for _, path := range paths {
		var bus *dbus.Conn
		bus, err = Connect(ctx, path)
		if err == nil {
			return bus, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}

func sessionSocketPaths(address, runtime string, uid int) ([]string, error) {
	if address == "" {
		if runtime == "" {
			runtime = filepath.Join("/run/user", strconv.Itoa(uid))
		}
		if !filepath.IsAbs(runtime) || strings.ContainsAny(runtime, "\x00\r\n") {
			return nil, errors.New("invalid session runtime directory")
		}
		return []string{strings.TrimRight(runtime, "/") + "/bus"}, nil
	}
	if len(address) > 64<<10 {
		return nil, errors.New("session bus address exceeds its limit")
	}
	addresses := strings.Split(address, ";")
	if len(addresses) > 16 {
		return nil, errors.New("session bus address exceeds its limit")
	}
	paths := make([]string, 0, len(addresses))
	for _, address := range addresses {
		path, err := unixSocketAddress(address)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func unixSocketAddress(address string) (string, error) {
	keys, ok := strings.CutPrefix(address, "unix:")
	if !ok {
		return "", errors.New("session bus requires a local Unix transport")
	}
	values := make(map[string]string, 3)
	for _, field := range strings.Split(keys, ",") {
		key, encoded, ok := strings.Cut(field, "=")
		if _, duplicate := values[key]; !ok || duplicate || (key != "path" && key != "abstract" && key != "guid") {
			return "", errors.New("invalid Unix bus address key")
		}
		value, err := decodeAddressValue(encoded)
		if err != nil || value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return "", errors.New("invalid Unix bus address value")
		}
		values[key] = value
	}
	if guid, present := values["guid"]; present {
		if decoded, err := hex.DecodeString(guid); err != nil || len(decoded) != 16 {
			return "", errors.New("invalid Unix bus address guid")
		}
	}
	path, abstract := values["path"], values["abstract"]
	if (path == "") == (abstract == "") || (path != "" && !filepath.IsAbs(path)) {
		return "", errors.New("local bus address requires one absolute path or abstract name")
	}
	if abstract != "" {
		return "\x00" + abstract, nil
	}
	return path, nil
}

func decodeAddressValue(encoded string) (string, error) {
	for i := 0; i < len(encoded); i++ {
		b := encoded[i]
		if b == '%' {
			i += 2
			continue
		}
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.ContainsRune("-_/.*", rune(b))) {
			return "", errors.New("unescaped Unix bus address byte")
		}
	}
	return url.PathUnescape(encoded)
}
