//go:build linux

package exec

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// relayedSignals are the signals the engine's guard sends a supervisor; the
// relay forwards each to the supervisor it stands in for. A relay the engine
// kills closes its call channel, and the broker ends that supervisor.
var relayedSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGCONT}

// parseRelayTarget decodes a relayed supervisor or a relayed prepared command
// into the extra-descriptor count and the request argv for the broker.
func parseRelayTarget(program string, args []string) (int, []string, bool) {
	if len(args) > 0 && args[0] == supervisorCommand {
		target, ok := parseSupervisorTarget(args[1:])
		if !ok {
			return 0, nil, false
		}
		// The supervisor's descriptors: the target's, its lifetime pipe and its
		// startup report.
		return target.extraFiles + 2, append([]string{program, supervisorCommand}, args[1:]...), true
	}
	if len(args) < 3 || args[1] == "" {
		return 0, nil, false
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 0 {
		return 0, nil, false
	}
	return count, append([]string{args[1]}, args[3:]...), true
}

// runBrokerRelay starts, through the protected launch's broker, the command
// or command supervisor its arguments describe, with its own standard streams
// and extra descriptors, and mirrors that process's exit. The broker is its
// last descriptor.
func runBrokerRelay(args []string) int {
	program, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return 125
	}
	extras, argv, ok := parseRelayTarget(program, args)
	if !ok {
		return 125
	}
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
	dir, err := os.Getwd()
	if err != nil {
		return 125
	}
	_, termErr := unix.IoctlGetTermios(0, unix.TCGETS)
	request := launchbroker.Request{
		Argv:        argv,
		Environment: os.Environ(),
		Dir:         dir,
		Terminal:    termErr == nil,
	}
	channel, err := launchbroker.Send(
		broker,
		request,
		os.NewFile(0, "relay-stdin"),
		os.NewFile(1, "relay-stdout"),
		os.NewFile(2, "relay-stderr"),
		files...,
	)
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
