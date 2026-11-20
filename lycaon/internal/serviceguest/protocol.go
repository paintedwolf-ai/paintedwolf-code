// Package serviceguest runs private service jobs over one VM-owned connection.
package serviceguest

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	Port        = 2663
	Version     = 1
	MaxOutput   = 32 << 10
	maxFrame    = 512 << 10
	maxArgBytes = 64 << 10
)

type State string

const (
	Running State = "running"
	Exited  State = "exited"
)

type Result struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          []byte `json:"stdout,omitempty"`
	Stderr          []byte `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
}

type Job struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	Result
}

type request struct {
	Version   int      `json:"version"`
	Sequence  uint64   `json:"sequence"`
	Op        string   `json:"op"`
	Challenge string   `json:"challenge,omitempty"`
	Argv      []string `json:"argv,omitempty"`
	ID        string   `json:"id,omitempty"`
}

type response struct {
	Version   int    `json:"version"`
	Sequence  uint64 `json:"sequence"`
	Challenge string `json:"challenge,omitempty"`
	Job       *Job   `json:"job,omitempty"`
	Error     string `json:"error,omitempty"`
}

func validArgv(argv []string) bool {
	if len(argv) == 0 || len(argv) > 256 || argv[0] == "" {
		return false
	}
	size := 0
	for _, arg := range argv {
		size += len(arg) + 1
		if strings.ContainsRune(arg, 0) || size > maxArgBytes {
			return false
		}
	}
	return true
}

func readFrame(r io.Reader, dst any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxFrame {
		return errors.New("invalid private service frame size")
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(buf))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid trailing private service data")
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return fmt.Errorf("private service frame exceeds %d bytes", maxFrame)
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	frame = append(frame, payload...)
	_, err = io.Copy(w, bytes.NewReader(frame))
	return err
}
