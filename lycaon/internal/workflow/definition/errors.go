package definition

import (
	"errors"
	"fmt"
)

type ExtendsErrorKind string

const (
	ExtendsErrorDepth   ExtendsErrorKind = "depth"
	ExtendsErrorUnknown ExtendsErrorKind = "unknown_parent"
	ExtendsErrorCycle   ExtendsErrorKind = "cycle"
)

// ExtendsError identifies a manifest-chain failure.
type ExtendsError struct {
	Kind            ExtendsErrorKind
	Ref             string
	ManifestID      string
	ManifestVersion string
	MaxDepth        int
}

func (e *ExtendsError) Error() string {
	if e == nil {
		return "workflow extends error"
	}
	prefix := ""
	if e.ManifestID != "" {
		prefix = fmt.Sprintf("workflow manifest %s@%s: ", e.ManifestID, e.ManifestVersion)
	}
	switch e.Kind {
	case ExtendsErrorDepth:
		return fmt.Sprintf("%sextends depth exceeds %d", prefix, e.MaxDepth)
	case ExtendsErrorUnknown:
		return fmt.Sprintf("%sunknown extends parent %s", prefix, e.Ref)
	case ExtendsErrorCycle:
		return fmt.Sprintf("%sextends cycle at %s", prefix, e.Ref)
	default:
		return "workflow extends error"
	}
}

var ErrUnknownWorkflow = errors.New("unknown workflow")

var ErrWorkflowParameterInvalid = errors.New("workflow parameter invalid")
