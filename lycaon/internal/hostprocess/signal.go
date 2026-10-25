package hostprocess

import "fmt"

// signalKind is the accepted operation, independent of native signal numbers.
type signalKind uint8

const (
	signalTerminate signalKind = iota + 1
	signalKill
	signalInterrupt
	signalHangup
	signalStop
	signalContinue
	signalUser1
	signalUser2
)

var signalNames = map[string]signalKind{
	"TERM": signalTerminate, "KILL": signalKill, "INT": signalInterrupt,
	"HUP": signalHangup, "STOP": signalStop, "CONT": signalContinue,
	"USR1": signalUser1, "USR2": signalUser2,
}

func parseSignal(name string) (signalKind, error) {
	if kind, ok := signalNames[name]; ok {
		return kind, nil
	}
	return 0, fmt.Errorf("unsupported signal %q", name)
}
