//go:build linux

package exec

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// relayedSignals are the signals the engine's guard sends a supervisor; the
// relay forwards each to the supervisor it stands in for. A relay the engine
// kills closes its call channel, and the broker ends that supervisor.
var relayedSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGCONT}

// runBrokerRelay starts, through the protected launch's broker, the command
// supervisor its arguments describe, with its own standard streams and the
// supervisor's descriptors, and mirrors that supervisor's exit. The broker
// is its last descriptor.
func runBrokerRelay(args []string) int {
	if len(args) < 1 || args[0] != supervisorCommand {
		return 125
	}
	target, ok := parseSupervisorTarget(args[1:])
	if !ok {
		return 125
	}
	// The supervisor's descriptors: the target's, its lifetime pipe and its
	// startup report.
	extras := target.extraFiles + 2
	brokerFD := 3 + extras
	if _, err := launchbroker.CheckSocket(brokerFD); err != nil {
		fmt.Fprintf(os.Stderr, "broker relay: %v\n", err)
		return 125
	}
	broker := os.NewFile(uintptr(brokerFD), "launch-broker")
	files := make([]*os.File, extras)
	for i := range files {
		fd := 3 + i
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			// A slot the parent left closed (a confinement helper pads its
			// descriptor layout this way) cannot travel as a right; the
			// supervisor receives /dev/null there, which no flag names.
			null, err := os.Open(os.DevNull)
			if err != nil {
				return 125
			}
			files[i] = null
			continue
		}
		files[i] = os.NewFile(uintptr(fd), "supervisor-descriptor")
	}
	events := make(chan os.Signal, 8)
	signal.Notify(events, relayedSignals...)
	program, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return 125
	}
	dir, err := os.Getwd()
	if err != nil {
		return 125
	}
	_, termErr := unix.IoctlGetTermios(0, unix.TCGETS)
	request := launchbroker.Request{
		Argv:        append([]string{program, supervisorCommand}, args[1:]...),
		Environment: os.Environ(),
		Dir:         dir,
		Terminal:    termErr == nil,
	}
	channel, err := launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...)
	// The supervisor holds its own copies now; the startup report must reach
	// end-of-file once that supervisor is done with it.
	for _, file := range files {
		_ = file.Close()
	}
	_ = broker.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "broker relay: %v\n", err)
		return 125
	}
	return relayEvents(channel, events)
}

// relayEvents forwards signals to the brokered supervisor and returns, or
// re-raises, its exit.
func relayEvents(channel *os.File, signals <-chan os.Signal) int {
	reports := make(chan launchbroker.Event, 2)
	go func() {
		for {
			event, err := launchbroker.ReadEvent(channel)
			if err != nil {
				reports <- launchbroker.Event{Error: "the broker closed the call before the supervisor exited"}
				return
			}
			reports <- event
			if event.Exited || event.Error != "" {
				return
			}
		}
	}()
	pid := 0
	var held []syscall.Signal
	for {
		select {
		case sig := <-signals:
			s, ok := sig.(syscall.Signal)
			switch {
			case !ok:
			case pid > 0:
				_ = unix.Kill(pid, s)
			default:
				// A signal before the start report waits for the pid.
				held = append(held, s)
			}
		case event := <-reports:
			switch {
			case event.Error != "":
				fmt.Fprintf(os.Stderr, "broker relay: %s\n", event.Error)
				return 125
			case event.Started > 0:
				pid = event.Started
				for _, s := range held {
					_ = unix.Kill(pid, s)
				}
				held = nil
			case event.Exited && event.Signal != 0:
				exitWithSignal(syscall.Signal(event.Signal))
				return 128 + event.Signal
			case event.Exited:
				return event.Code
			}
		}
	}
}
