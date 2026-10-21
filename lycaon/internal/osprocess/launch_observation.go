package osprocess

import (
	"context"
	"fmt"
	"os"
)

// ProcessSecurity reports kernel credentials, not a host permission or person.
type ProcessSecurity struct {
	RealUID, EffectiveUID, SavedUID uint32
	RealGID, EffectiveGID, SavedGID uint32
	Linux                           *LinuxPrivileges
}

// LinuxPrivileges distinguishes observed capability sets and no-new-privileges.
type LinuxPrivileges struct {
	Permitted, Effective, Ambient uint64
	Inheritable, Bounding         uint64
	NoNewPrivileges               bool
}

// LaunchObservation is a stopped process snapshot consumed by the launch guard.
// Image, cwd, arguments/environment and credentials remain separate facts.
type LaunchObservation struct {
	ExecutableMatches bool
	DirectoryMatches  bool
	Arguments         []string
	Environment       []string
	// EnvironmentObserved distinguishes returned environment bytes from native
	// omission. Darwin can hide even an owned restricted process environment;
	// a zero-length result there is conservatively unknown, never known empty.
	EnvironmentObserved bool
	Security            ProcessSecurity
}

// ObserveStoppedLaunch requires bounded native argument capture and a current
// stopped image on both sides of the snapshot. It never releases the process.
func ObserveStoppedLaunch(ctx context.Context, pid int, executable, cwd *os.File, maximumBytes int) (LaunchObservation, error) {
	if ctx == nil || maximumBytes <= 0 || maximumBytes > 32<<20 {
		return LaunchObservation{}, ErrExecutableObservationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return LaunchObservation{}, err
	}
	before, ok := StartTime(pid)
	if !ok {
		return LaunchObservation{}, ErrExecutableObservationUnavailable
	}
	image, err := MatchStoppedExecutable(pid, executable)
	if err != nil {
		return LaunchObservation{}, err
	}
	directory, err := matchProcessDirectory(pid, cwd)
	if err != nil {
		return LaunchObservation{}, err
	}
	args, env, environmentObserved, err := processArguments(pid, maximumBytes)
	if err != nil {
		return LaunchObservation{}, err
	}
	security, err := processSecurity(pid)
	if err != nil {
		return LaunchObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return LaunchObservation{}, err
	}
	after, ok := StartTime(pid)
	if !ok || before != after {
		return LaunchObservation{}, ErrExecutableObservationUnavailable
	}
	current, err := MatchStoppedExecutable(pid, executable)
	if err != nil {
		return LaunchObservation{}, err
	}
	if current != image {
		return LaunchObservation{}, ErrExecutableObservationUnavailable
	}
	return LaunchObservation{ExecutableMatches: image, DirectoryMatches: directory, Arguments: args, Environment: env, EnvironmentObserved: environmentObserved, Security: security}, nil
}

// ProcessCredentials captures the current incarnation's observed credentials.
func ProcessCredentials(pid int) (ProcessSecurity, error) {
	before, ok := StartTime(pid)
	if !ok {
		return ProcessSecurity{}, ErrExecutableObservationUnavailable
	}
	result, err := processSecurity(pid)
	if err != nil {
		return ProcessSecurity{}, err
	}
	if after, ok := StartTime(pid); !ok || after != before {
		return ProcessSecurity{}, fmt.Errorf("%w: process incarnation changed", ErrExecutableObservationUnavailable)
	}
	return result, nil
}
