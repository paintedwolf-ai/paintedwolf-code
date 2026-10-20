package confine

import (
	"fmt"
	"strings"
)

// LaunchVectorLimits comes from the compiled language ceilings. This necessary
// per-dispatch check does not charge expansion twice or prove aggregate usage;
// aggregate field/state reservations remain the evaluator's obligation.
type LaunchVectorLimits struct {
	ArgumentBytes    int64
	ArgvBytes        int64
	EnvironmentBytes int64
	ExpandedFields   int64
}

// LaunchVectorLimitError identifies a refusal before vector copying or spawn.
type LaunchVectorLimitError struct {
	Dimension         string
	Maximum, Observed int64
}

func (e *LaunchVectorLimitError) Error() string {
	return fmt.Sprintf("launch %s exceeds bound", e.Dimension)
}

func (l LaunchVectorLimits) validate() error {
	if l.ArgumentBytes <= 0 || l.ArgvBytes <= 0 || l.EnvironmentBytes <= 0 || l.ExpandedFields <= 0 {
		return fmt.Errorf("launch requires explicit positive vector limits")
	}
	return nil
}
func (l LaunchVectorLimits) check(args, environment []string) error {
	if err := l.validate(); err != nil {
		return err
	}
	if int64(len(args)) > l.ExpandedFields {
		return &LaunchVectorLimitError{"expanded_fields", l.ExpandedFields, int64(len(args))}
	}
	for i, vector := range [][]string{args, environment} {
		var bytes int64
		for _, value := range vector {
			if strings.IndexByte(value, 0) >= 0 {
				return fmt.Errorf("launch vector contains a NUL scalar")
			}
			n := int64(len(value))
			if i == 0 && n > l.ArgumentBytes {
				return &LaunchVectorLimitError{"argument_bytes", l.ArgumentBytes, n}
			}
			// Payload bytes implement language accounting. The platform separately
			// admits terminators and pointer tables against its actual OS launch bound.
			maximum, dimension := l.ArgvBytes, "argv_bytes"
			if i == 1 {
				maximum, dimension = l.EnvironmentBytes, "environment_bytes"
			}
			if n > maximum-bytes {
				return &LaunchVectorLimitError{dimension, maximum, bytes + n}
			}
			bytes += n
		}
	}
	return nil
}
