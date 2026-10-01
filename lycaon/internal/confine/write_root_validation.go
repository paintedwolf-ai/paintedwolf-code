package confine

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/fspath"
)

// ErrWriteRootRefused marks a root rejected at an execution boundary.
var ErrWriteRootRefused = errors.New("confine: write root refused")

// WriteRootRefusalError carries a refusal code and path.
type WriteRootRefusalError struct {
	Path string
	Code string
}

func (e *WriteRootRefusalError) Error() string {
	if e == nil || e.Code == "" {
		return ErrWriteRootRefused.Error()
	}
	return fmt.Sprintf("%s: %s", ErrWriteRootRefused, e.Code)
}

func (e *WriteRootRefusalError) Unwrap() error { return ErrWriteRootRefused }

// ValidateAttachedWriteRoots rejects unsafe standing roots without changing
// their durable records.
func ValidateAttachedWriteRoots(roots []string) *WriteRootRefusalError {
	return validateWriteRootList(roots, AttachedWriteRootRefused)
}

// ValidateGrantedWriteRoots rejects per-action lease roots the boundary could
// not apply.
func ValidateGrantedWriteRoots(roots []string) *WriteRootRefusalError {
	return validateWriteRootList(roots, GrantedWriteRootRefused)
}

// validateWriteRootList applies one refusal rule to canonical roots.
func validateWriteRootList(roots []string, refusal func(string) (bool, string)) *WriteRootRefusalError {
	for _, root := range normalizePathList(roots) {
		canonical := fspath.CanonicalPath(root)
		if canonical == "" {
			continue
		}
		if refused, code := refusal(canonical); refused {
			return &WriteRootRefusalError{Path: root, Code: code}
		}
	}
	return nil
}
