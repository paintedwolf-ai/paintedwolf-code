// Package gitcredential implements Git's credential-helper protocol for the
// host-managed macOS Keychain bridge.
package gitcredential

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const maxCredentialInputBytes = 64 * 1024

// Credential is the bounded subset stored by the host.
// Unknown protocol fields are ignored.
type Credential struct {
	Protocol string
	Host     string
	Path     string
	Username string
	Password string
}

type backend interface {
	Get(Credential) (Credential, bool, error)
	Store(Credential) error
	Erase(Credential) error
}

// Run executes one helper operation over Git's line-oriented stdin/stdout protocol.
func Run(operation string, in io.Reader, out io.Writer) error {
	return runWithBackend(operation, in, out, newBackend())
}

func runWithBackend(operation string, in io.Reader, out io.Writer, store backend) error {
	credential, err := parse(io.LimitReader(in, maxCredentialInputBytes+1))
	if err != nil {
		return err
	}
	switch operation {
	case "get":
		resolved, ok, err := store.Get(credential)
		if err != nil || !ok {
			return err
		}
		return writeCredential(out, resolved)
	case "store":
		if credential.Protocol == "" || credential.Host == "" || credential.Username == "" || credential.Password == "" {
			return nil
		}
		return store.Store(credential)
	case "erase":
		return store.Erase(credential)
	default:
		return fmt.Errorf("unsupported credential operation %q", operation)
	}
}

func parse(in io.Reader) (Credential, error) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxCredentialInputBytes)
	credential := Credential{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "protocol":
			credential.Protocol = value
		case "host":
			credential.Host = value
		case "path":
			credential.Path = value
		case "username":
			credential.Username = value
		case "password":
			credential.Password = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Credential{}, fmt.Errorf("read credential request: %w", err)
	}
	for name, value := range map[string]string{
		"protocol": credential.Protocol,
		"host":     credential.Host,
		"path":     credential.Path,
		"username": credential.Username,
		"password": credential.Password,
	} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return Credential{}, fmt.Errorf("credential %s contains a control character", name)
		}
	}
	return credential, nil
}

func writeCredential(out io.Writer, credential Credential) error {
	writer := bufio.NewWriter(out)
	for _, pair := range [][2]string{
		{"username", credential.Username},
		{"password", credential.Password},
	} {
		if pair[1] == "" {
			continue
		}
		if _, err := fmt.Fprintf(writer, "%s=%s\n", pair[0], pair[1]); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString("\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func splitHostPort(host string) (string, int32) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), 0
	}
	separator := strings.LastIndexByte(host, ':')
	if separator <= 0 || strings.Count(host, ":") != 1 {
		return host, 0
	}
	port, err := strconv.ParseUint(host[separator+1:], 10, 16)
	if err != nil || port == 0 {
		return host, 0
	}
	return host[:separator], int32(port)
}

func keychainProtocol(protocol string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "http":
		return "http", nil
	case "https":
		return "htps", nil
	default:
		return "", errors.New("credential protocol must be http or https")
	}
}
