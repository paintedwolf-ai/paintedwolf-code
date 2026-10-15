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

// SessionSource names the fact a session bus selection came from.
type SessionSource string

const (
	// SessionSourceAddress is the launcher's DBUS_SESSION_BUS_ADDRESS.
	SessionSourceAddress SessionSource = "DBUS_SESSION_BUS_ADDRESS"
	// SessionSourceRuntimeDirectory is the bus socket inside XDG_RUNTIME_DIR.
	SessionSourceRuntimeDirectory SessionSource = "XDG_RUNTIME_DIR"
	// SessionSourceDefaultRuntime is the per-uid runtime directory convention.
	SessionSourceDefaultRuntime SessionSource = "default runtime directory"
)

// SocketAddress is one local bus endpoint.
type SocketAddress struct {
	// Path is a filesystem socket, or the abstract name when Abstract is set.
	Path     string
	Abstract bool
}

func (a SocketAddress) dialAddress() string {
	if a.Abstract {
		return "\x00" + a.Path
	}
	return a.Path
}

// String renders the address for diagnostics.
func (a SocketAddress) String() string {
	if a.Abstract {
		return "abstract:" + a.Path
	}
	return a.Path
}

// Session is the explicit session bus selection for this process.
type Session struct {
	Source SessionSource
	// Addresses are tried in order; a launcher may list alternatives.
	Addresses []SocketAddress
}

// SelectSession resolves the session bus from the inherited environment
// without launching a bus or searching directories.
func SelectSession(lookupEnv func(string) (string, bool), uid int) (Session, error) {
	if address, ok := lookupEnv("DBUS_SESSION_BUS_ADDRESS"); ok && address != "" {
		addresses, err := parseAddresses(address)
		if err != nil {
			return Session{}, err
		}
		return Session{Source: SessionSourceAddress, Addresses: addresses}, nil
	}
	source := SessionSourceRuntimeDirectory
	runtime, ok := lookupEnv("XDG_RUNTIME_DIR")
	if !ok || runtime == "" {
		source = SessionSourceDefaultRuntime
		runtime = filepath.Join("/run/user", strconv.Itoa(uid))
	}
	if !filepath.IsAbs(runtime) || strings.ContainsAny(runtime, "\x00\r\n") {
		return Session{}, errors.New("invalid session runtime directory")
	}
	return Session{Source: source, Addresses: []SocketAddress{{Path: strings.TrimRight(runtime, "/") + "/bus"}}}, nil
}

// Connect opens the first reachable address within one bounded deadline.
func (s Session) Connect(ctx context.Context) (*dbus.Conn, error) {
	if len(s.Addresses) == 0 {
		return nil, errors.New("session bus has no address")
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var err error
	for _, address := range s.Addresses {
		var bus *dbus.Conn
		bus, err = Connect(ctx, address.dialAddress())
		if err == nil {
			return bus, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}

// ConnectSession uses the inherited session selection without launching a bus.
func ConnectSession(ctx context.Context) (*dbus.Conn, error) {
	session, err := SelectSession(os.LookupEnv, os.Getuid())
	if err != nil {
		return nil, err
	}
	return session.Connect(ctx)
}

func parseAddresses(address string) ([]SocketAddress, error) {
	if len(address) > 64<<10 {
		return nil, errors.New("bus address exceeds its limit")
	}
	alternatives := strings.Split(address, ";")
	if len(alternatives) > 16 {
		return nil, errors.New("bus address exceeds its limit")
	}
	addresses := make([]SocketAddress, 0, len(alternatives))
	for _, alternative := range alternatives {
		parsed, err := unixSocketAddress(alternative)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, parsed)
	}
	return addresses, nil
}

func unixSocketAddress(address string) (SocketAddress, error) {
	keys, ok := strings.CutPrefix(address, "unix:")
	if !ok {
		return SocketAddress{}, errors.New("local bus requires a Unix transport")
	}
	values := make(map[string]string, 3)
	for _, field := range strings.Split(keys, ",") {
		key, encoded, ok := strings.Cut(field, "=")
		if _, duplicate := values[key]; !ok || duplicate || (key != "path" && key != "abstract" && key != "guid") {
			return SocketAddress{}, errors.New("invalid Unix bus address key")
		}
		value, err := decodeAddressValue(encoded)
		if err != nil || value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return SocketAddress{}, errors.New("invalid Unix bus address value")
		}
		values[key] = value
	}
	if guid, present := values["guid"]; present {
		if decoded, err := hex.DecodeString(guid); err != nil || len(decoded) != 16 {
			return SocketAddress{}, errors.New("invalid Unix bus address guid")
		}
	}
	path, abstract := values["path"], values["abstract"]
	if (path == "") == (abstract == "") || (path != "" && !filepath.IsAbs(path)) {
		return SocketAddress{}, errors.New("local bus address requires one absolute path or abstract name")
	}
	if abstract != "" {
		return SocketAddress{Path: abstract, Abstract: true}, nil
	}
	return SocketAddress{Path: path}, nil
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
