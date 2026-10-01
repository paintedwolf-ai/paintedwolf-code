package startupprotocol

import (
	"errors"
	"io"
)

// ControlStdinEnv enables the private desktop control pipe.
const ControlStdinEnv = "LYCAON_CONTROL_STDIN"

var shutdownFrame = [...]byte{'s', 'h', 'u', 't', 'd', 'o', 'w', 'n', '\n'}

// ReadControlShutdown reads one complete shutdown frame.
func ReadControlShutdown(r io.Reader) error {
	var frame [len(shutdownFrame)]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return err
	}
	if frame != shutdownFrame {
		return errors.New("invalid desktop control frame")
	}
	return nil
}
