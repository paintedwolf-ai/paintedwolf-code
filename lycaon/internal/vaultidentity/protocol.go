// Package vaultidentity is the wire between the engine and the packaged
// vault identity service: length-prefixed JSON over a Unix socket, where
// the service authenticates the caller from its credentials and process
// image rather than from anything in the request.
package vaultidentity

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const (
	// SocketPath is where the socket unit listens. Any user may connect; the
	// service decides from the peer's credentials and process image.
	SocketPath = "/run/painted-wolf-code/vault-identity.sock"

	// MaxValueBytes bounds one stored identity; a frame also carries its
	// base64 expansion and the envelope.
	MaxValueBytes = 64 << 10
	maxFrameBytes = 2 * MaxValueBytes
)

// Op is one request kind.
type Op string

const (
	// OpProbe verifies the caller and returns the launcher it verified,
	// touching no stored identity.
	OpProbe Op = "probe"
	OpRead  Op = "read"
	// OpCreate stores a new identity; an existing one is never replaced.
	OpCreate Op = "create"
	OpRemove Op = "remove"
)

// Request is one engine request. Value is base64 in JSON.
type Request struct {
	Op      Op     `json:"op"`
	Account string `json:"account,omitempty"`
	Value   []byte `json:"value,omitempty"`
}

// Status classifies a response.
type Status string

const (
	StatusOK       Status = "ok"
	StatusNotFound Status = "not_found"
	StatusExists   Status = "exists"
	// StatusRefused: the caller is not a verified engine under the installed
	// desktop app. Code carries the launchverify reason.
	StatusRefused Status = "refused"
	// StatusUnavailable: the service cannot serve anyone on this host.
	StatusUnavailable Status = "unavailable"
	StatusInvalid     Status = "invalid"
)

// Response is the service's answer.
type Response struct {
	Status  Status `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Value   []byte `json:"value,omitempty"`
	// LauncherPID is the desktop app the service verified as the caller's
	// parent, on every successful response.
	LauncherPID int `json:"launcher_pid,omitempty"`
}

// Err converts a non-ok response into an error.
func (r Response) Err() error {
	if r.Status == StatusOK {
		return nil
	}
	return &StatusError{Status: r.Status, Code: r.Code, Message: r.Message}
}

// StatusError is a non-ok response as an error.
type StatusError struct {
	Status  Status
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("vault identity service: %s (%s: %s)", e.Status, e.Code, e.Message)
	case e.Code != "":
		return fmt.Sprintf("vault identity service: %s (%s)", e.Status, e.Code)
	default:
		return fmt.Sprintf("vault identity service: %s", e.Status)
	}
}

// StatusOf returns the status err carries, or "" for other errors.
func StatusOf(err error) Status {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status
	}
	return ""
}

var accountPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidAccount reports whether account is a usable identity name: one
// path segment the service can store as a file name.
func ValidAccount(account string) bool { return accountPattern.MatchString(account) }

// WriteFrame writes v as one length-prefixed JSON frame.
func WriteFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	if len(body) > maxFrameBytes {
		return fmt.Errorf("frame exceeds %d bytes", maxFrameBytes)
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	_, err = w.Write(frame)
	return err
}

// ReadFrame reads one frame into v.
func ReadFrame(r io.Reader, v any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > maxFrameBytes {
		return fmt.Errorf("frame length %d is out of bounds", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode frame: %w", err)
	}
	return nil
}
