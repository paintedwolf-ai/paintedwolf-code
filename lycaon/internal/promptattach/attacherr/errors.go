// Package attacherr defines typed attachment rejections.
package attacherr

import (
	"errors"
	"fmt"
)

const (
	CodeUnsupported = "unsupported_attachment"
	CodeTooLarge    = "attachment_too_large"
	CodeOutOfJail   = "reference_out_of_jail"
	// CodeUndecodable means an admitted format holds content the host cannot decode.
	CodeUndecodable = "attachment_undecodable"
	// CodeNotFound means a blob ID cannot be resolved.
	CodeNotFound = "attachment_not_found"
)

// Error is a typed attachment reject with a structured Code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func Unsupported(msg string) error { return &Error{Code: CodeUnsupported, Message: msg} }
func TooLarge(msg string) error    { return &Error{Code: CodeTooLarge, Message: msg} }
func OutOfJail(msg string) error   { return &Error{Code: CodeOutOfJail, Message: msg} }
func NotFound(msg string) error    { return &Error{Code: CodeNotFound, Message: msg} }
func Undecodable(msg string) error { return &Error{Code: CodeUndecodable, Message: msg} }

// CodeOf returns the structured Code for an attacherr.Error, or "".
func CodeOf(err error) string {
	e := &Error{}
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
