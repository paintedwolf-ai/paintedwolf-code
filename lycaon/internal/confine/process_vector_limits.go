package confine

import (
	"fmt"
	"strings"
	"syscall"
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

// PlatformLaunchLimitError distinguishes a native OS vector ceiling from a
// compiled language resource limit. The native errno remains inspectable.
type PlatformLaunchLimitError struct{ Maximum, Observed int64 }

func (e *PlatformLaunchLimitError) Error() string { return "native argv/environment bound exceeded" }
func (e *PlatformLaunchLimitError) Unwrap() error { return syscall.E2BIG }

func (l LaunchVectorLimits) validate() error {
	if l.ArgumentBytes <= 0 || l.ArgvBytes <= 0 || l.EnvironmentBytes <= 0 || l.ExpandedFields <= 0 {
		return fmt.Errorf("launch requires explicit positive vector limits")
	}
	return nil
}
func (l LaunchVectorLimits) checkCommand(name string, args, environment []string) error {
	if err := l.validate(); err != nil {
		return err
	}
	fields := int64(len(args)) + 1
	if fields > l.ExpandedFields {
		return &LaunchVectorLimitError{"expanded_fields", l.ExpandedFields, fields}
	}
	var argumentBytes int64
	for _, group := range [][]string{{name}, args} {
		for _, value := range group {
			if strings.IndexByte(value, 0) >= 0 {
				return fmt.Errorf("launch vector contains a NUL scalar")
			}
			n := int64(len(value))
			if n > l.ArgumentBytes {
				return &LaunchVectorLimitError{"argument_bytes", l.ArgumentBytes, n}
			}
			if n > l.ArgvBytes-argumentBytes {
				return &LaunchVectorLimitError{"argv_bytes", l.ArgvBytes, argumentBytes + n}
			}
			argumentBytes += n
		}
	}
	var environmentBytes int64
	for _, value := range environment {
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("launch vector contains a NUL scalar")
		}
		n := int64(len(value))
		if n > l.EnvironmentBytes-environmentBytes {
			return &LaunchVectorLimitError{"environment_bytes", l.EnvironmentBytes, environmentBytes + n}
		}
		environmentBytes += n
	}
	return nil
}

// NativeVectorLimits is the actual platform allowance for retained native
// producers. Command evaluators also supply their stricter compiled ceilings.
func NativeVectorLimits() (LaunchVectorLimits, error) {
	maximum, err := nativeArgumentBudget()
	if err != nil {
		return LaunchVectorLimits{}, err
	}
	if maximum <= 0 {
		return LaunchVectorLimits{}, fmt.Errorf("native argument allowance unavailable")
	}
	n := int64(maximum)
	return LaunchVectorLimits{ArgumentBytes: n, ArgvBytes: n, EnvironmentBytes: n, ExpandedFields: n / 8}, nil
}
