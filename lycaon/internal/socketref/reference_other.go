//go:build !linux && !darwin

package socketref

import "os"

func Platform() Adapter { return Adapter{Command: NoRealization, Native: NoRealization} }

func implicitBindingMode() Enforcement { return NoRealization }

func ControlPlanePathPattern() string { return "" }

func Open(string) (*Reference, error) { return nil, ErrUnsupported }

func newReferenceEndpoint(*Reference) (serviceEndpoint, error) { return nil, ErrUnsupported }

func descriptorPath(*os.File) string { return "" }

func duplicate(*os.File) (*os.File, error) { return nil, ErrUnbound }

func sameObject(*Reference, *Reference) bool { return false }

func matchesPath(*Reference, string) bool { return false }
