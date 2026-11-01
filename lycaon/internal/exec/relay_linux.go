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

// parseRelayTarget decodes a relayed supervisor or a relayed prepared command
// into the explicit descriptor layout and the request argv for the broker.
func parseRelayTarget(program string, args []string) (string, []string, bool) {
	if len(args) > 0 && args[0] == supervisorCommand {
		target, ok := parseSupervisorTarget(args[1:])
		if !ok {
			return "", nil, false
		}
		// The supervisor's descriptors: the target's, its lifetime pipe and its
		// startup report.
		layout := target.descriptors + "11"
		return layout, append([]string{program, supervisorCommand}, args[1:]...), validFileLayout(layout)
	}
	if len(args) < 3 || args[1] == "" {
		return "", nil, false
	}
	if !validFileLayout(args[0]) {
		return "", nil, false
	}
	return args[0], append([]string{args[1]}, args[3:]...), true
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
	layout, argv, ok := parseRelayTarget(program, args)
	if !ok {
		return 125
	}
	brokerFD := 3 + len(layout)
	if _, err := launchbroker.CheckSocket(brokerFD); err != nil {
		fmt.Fprintf(os.Stderr, "broker relay: %v\n", err)
		return 125
	}
	broker := os.NewFile(uintptr(brokerFD), "launch-broker")
	files, err := relayDescriptorFiles(layout)
	if err != nil {
		return 125
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
		os.Stdin,
		os.Stdout,
		os.Stderr,
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

func relayDescriptorFiles(layout string) ([]*os.File, error) {
	if !validFileLayout(layout) {
		return nil, fmt.Errorf("invalid inherited descriptor layout")
	}
	files := make([]*os.File, len(layout))
	for i := range files {
		if layout[i] == '1' {
			files[i] = os.NewFile(uintptr(3+i), "supervisor-descriptor")
			continue
		}
		// SCM_RIGHTS needs a real descriptor in each transport slot. The target
		// still sees a closed slot from the explicit supervisor layout.
		file, err := os.Open(os.DevNull)
		if err != nil {
			for _, opened := range files {
				if opened != nil {
					_ = opened.Close()
				}
			}
			return nil, err
		}
		files[i] = file
	}
	return files, nil
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
