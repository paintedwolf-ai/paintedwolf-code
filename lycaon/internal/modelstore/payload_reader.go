package modelstore

import (
	"context"
	"errors"
	"io"
	"time"
)

const payloadReadTimeout = 90 * time.Second

var errPayloadStalled = errors.New("modelstore: download stopped receiving data")

type payloadReader struct {
	body  io.Reader
	timer *time.Timer
}

// Bound each network read independently of local writes and total transfer time.
func (r payloadReader) Read(p []byte) (int, error) {
	r.timer.Reset(payloadReadTimeout)
	n, err := r.body.Read(p)
	r.timer.Stop()
	return n, err
}

func copyPayload(dst io.Writer, body io.Reader, cancel context.CancelCauseFunc) (int64, error) {
	timer := time.AfterFunc(payloadReadTimeout, func() { cancel(errPayloadStalled) })
	timer.Stop()
	return io.Copy(dst, payloadReader{body: body, timer: timer})
}
