// Package noticeerr carries notice codes through error chains.
package noticeerr

import (
	"errors"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// Coded is implemented by errors that map to one notice code.
type Coded interface {
	error
	NoticeCode() wire.NoticeCode
}

// CodeOf reports the code carried anywhere in err's chain.
func CodeOf(err error) (wire.NoticeCode, bool) {
	if err == nil {
		return "", false
	}
	var coded Coded
	if errors.As(err, &coded) && coded.NoticeCode() != "" {
		return coded.NoticeCode(), true
	}
	return "", false
}

// Sentinel is a package-level error value that carries a notice code.
type Sentinel struct {
	msg  string
	code wire.NoticeCode
}

// NewSentinel builds a coded sentinel comparable with errors.Is.
func NewSentinel(msg string, code wire.NoticeCode) *Sentinel {
	return &Sentinel{msg: msg, code: code}
}

func (s *Sentinel) Error() string { return s.msg }

func (s *Sentinel) NoticeCode() wire.NoticeCode { return s.code }

// carrier reattaches a code after type loss.
type carrier struct {
	err  error
	code wire.NoticeCode
}

func (c carrier) Error() string { return c.err.Error() }

func (c carrier) Unwrap() error { return c.err }

func (c carrier) NoticeCode() wire.NoticeCode { return c.code }

// WithCode returns err carrying code. An empty code returns err unchanged.
func WithCode(err error, code wire.NoticeCode) error {
	if err == nil || code == "" {
		return err
	}
	return carrier{err: err, code: code}
}
