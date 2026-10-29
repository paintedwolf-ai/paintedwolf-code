//go:build (!darwin && !linux) || (darwin && !cgo)

package hostprocess

func listPIDs() ([]int, error)                 { return nil, ErrUnsupported }
func inspect(int) (Process, error)             { return Process{}, ErrUnsupported }
func PhysicalMemoryBytes() uint64              { return 0 }
func signalInstance(Process, signalKind) error { return ErrUnsupported }
func readCommandLine(int) string               { return "" }
func processExistsFallback(int) bool           { return false }
