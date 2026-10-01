//go:build (!darwin && !linux) || (darwin && !cgo)

package hostprocess

func listPIDs() ([]int, error)             { return nil, ErrUnsupported }
func inspect(int) (Process, error)         { return Process{}, ErrUnsupported }
func signalInstance(Process, string) error { return ErrUnsupported }
