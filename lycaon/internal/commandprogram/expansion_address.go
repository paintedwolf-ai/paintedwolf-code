package commandprogram

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// LogicalAddresses is implemented by a Dispatch frame whose unquoted
// `@label` and `@scratch` word prefixes name attached folders. Address
// returns the folder's native absolute path; ok false leaves the word
// literal. Conversion grants nothing: the operation or launch that uses the
// path checks its authority. An error is a typed host refusal.
type LogicalAddresses interface {
	Address(ctx context.Context, label string) (native string, ok bool, err error)
}

// DirectoryFrame is implemented by a Dispatch frame with a working
// directory: the absolute path PWD holds after each cd.
type DirectoryFrame interface {
	Directory() string
}

// AddressError is a host refusal of an @label word prefix, located at the
// word. Cause carries the owner's typed rejection.
type AddressError struct {
	Label string
	Span  toolcontract.SourceSpan
	Cause error
}

func (e *AddressError) Error() string { return "@" + e.Label + ": " + e.Cause.Error() }
func (e *AddressError) Unwrap() error { return e.Cause }

// addressScope is the frame view expansion asks to convert a prefix.
type addressScope interface {
	Address(ctx context.Context, label string) (string, bool, error)
}

// address expands an unquoted leading `@label` of a word's first literal
// part, as tilde expansion does for `~`: the label ends at the first `/`
// or, when the literal is the whole word, at its end. Escaped, quoted or
// pattern text in the label leaves the word literal.
func (r *expansionRun) address(text string, whole bool) (string, error) {
	scope, ok := r.scope.(addressScope)
	if !ok || !strings.HasPrefix(text, "@") {
		return text, nil
	}
	label, rest, found := strings.Cut(text[1:], "/")
	if !found && !whole {
		return text, nil
	}
	if label == "" || strings.ContainsAny(label, "\\'\"$`*?[") {
		return text, nil
	}
	native, ok, err := scope.Address(r.ctx, label)
	if err != nil {
		return "", &AddressError{Label: label, Span: r.span, Cause: err}
	}
	if !ok {
		return text, nil
	}
	if err := r.assembly.add(native, true); err != nil {
		return "", err
	}
	if found {
		return "/" + rest, nil
	}
	return "", nil
}
