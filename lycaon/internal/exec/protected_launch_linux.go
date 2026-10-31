//go:build linux

package exec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/launchbroker"
)

// LaunchBrokerFDEnv names the descriptor on which the desktop hands the
// engine the protected launch's broker. The engine reads it only when its own
// user namespace shows the protected launch.
const LaunchBrokerFDEnv = "LYCAON_LAUNCH_BROKER_FD"

// initialUserNamespaceInode is the kernel's PROC_USER_INIT_INO.
const initialUserNamespaceInode = 0xEFFFFFFD

// relayCommand is the engine image's broker relay mode.
const relayCommand = "__broker-relay"

// ErrLaunchBrokerUnavailable refuses a launch inside the protected namespace
// when the broker that must start it is missing.
var ErrLaunchBrokerUnavailable = errors.New("the protected launch broker is unavailable, so the program cannot start")

// protectedLaunch is this process's position relative to the protected
// launch, decided once from kernel facts.
type protectedLaunch struct {
	protected bool
	broker    *os.File
	err       error
}

func init() {
	lookExecutable = func(path string) (string, error) {
		if protectedState().protected {
			return relayedExecutable(path)
		}
		return exec.LookPath(path)
	}
	// Confinement availability is probed where confined commands run: under
	// the protected launch, outside its namespace through the broker.
	confine.SetHelperProbeRunner(func(cmd *exec.Cmd) error {
		if protectedState().protected {
			return runHelperProbeGroup(cmd)
		}
		return cmd.Run()
	})
}

var protectedState = sync.OnceValue(func() protectedLaunch {
	return detectProtectedLaunch(namespaceFacts, os.Getenv(LaunchBrokerFDEnv))
})

// userNamespaceFacts are what decide the protected launch: a user namespace
// owned by uid 0 that has a parent, mapping every identity to itself.
type userNamespaceFacts struct {
	owner    int
	nested   bool
	identity bool
}

func (f userNamespaceFacts) protected() bool { return f.nested && f.owner == 0 && f.identity }

func namespaceFacts() (userNamespaceFacts, error) {
	own, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return userNamespaceFacts{}, err
	}
	defer func() { _ = own.Close() }()
	owner, err := unix.IoctlGetInt(int(own.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil {
		return userNamespaceFacts{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(own.Fd()), &stat); err != nil {
		return userNamespaceFacts{}, err
	}
	// The kernel numbers the initial user namespace with a fixed inode; a
	// child's NS_GET_PARENT answers EPERM too when its parent lies outside the
	// caller's view, so the parent query cannot tell them apart.
	facts := userNamespaceFacts{owner: owner, nested: stat.Ino != initialUserNamespaceInode}
	raw, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return userNamespaceFacts{}, err
	}
	facts.identity = strings.Join(strings.Fields(string(raw)), " ") == "0 0 4294967295"
	return facts, nil
}

// detectProtectedLaunch accepts the broker only from a process inside the
// protected namespace, and only a broker created outside it: nobody inside
// is uid 0, so a socket pair whose creator was uid 0 came from the bootstrap.
func detectProtectedLaunch(read func() (userNamespaceFacts, error), named string) protectedLaunch {
	facts, err := read()
	if err != nil {
		return protectedLaunch{err: fmt.Errorf("read user namespace facts: %w", err)}
	}
	if !facts.protected() {
		return protectedLaunch{}
	}
	state := protectedLaunch{protected: true}
	fd, err := strconv.Atoi(strings.TrimSpace(named))
	if err != nil || fd < 3 {
		state.err = fmt.Errorf("%w: no broker descriptor was handed to the engine", ErrLaunchBrokerUnavailable)
		return state
	}
	peer, err := launchbroker.CheckSocket(fd)
	if err != nil {
		state.err = fmt.Errorf("%w: %w", ErrLaunchBrokerUnavailable, err)
		return state
	}
	if peer.Uid != 0 {
		state.err = fmt.Errorf("%w: the broker socket was not created by the bootstrap", ErrLaunchBrokerUnavailable)
		return state
	}
	unix.CloseOnExec(fd)
	state.broker = os.NewFile(uintptr(fd), "launch-broker")
	return state
}

// relayThroughBroker turns a command into its broker relay when this engine
// runs inside the protected launch. The relay receives the broker as its last
// descriptor; everything else is the command's or its supervisor's.
func relayThroughBroker(cmd *exec.Cmd) error { return relayThrough(cmd, protectedState()) }

func relayThrough(cmd *exec.Cmd, state protectedLaunch) error {
	if !state.protected {
		// Outside the protected launch an unreadable namespace changes
		// nothing: the command runs as it always has.
		return nil
	}
	if state.err != nil {
		return state.err
	}
	if commandSupervisor(cmd) {
		cmd.Args = append([]string{cmd.Args[0], relayCommand}, cmd.Args[1:]...)
		cmd.ExtraFiles = append(cmd.ExtraFiles, state.broker)
		return nil
	}
	resolveRelayedProgram(cmd, state)
	self, _ := os.Executable()
	count := len(cmd.ExtraFiles)
	cmd.Args = append([]string{self, relayCommand, strconv.Itoa(count), cmd.Path}, cmd.Args...)
	cmd.Path = self
	cmd.ExtraFiles = append(cmd.ExtraFiles, state.broker)
	return nil
}

// unrelayCommand restores a prepared command before superviseCommand wraps it
// in a supervisor and relays the supervisor instead.
func unrelayCommand(cmd *exec.Cmd) {
	if !relayedCommand(cmd) || commandSupervisor(cmd) || len(cmd.Args) < 5 {
		return
	}
	count, err := strconv.Atoi(cmd.Args[2])
	if err != nil || count < 0 || len(cmd.ExtraFiles) <= count {
		return
	}
	cmd.Path = cmd.Args[3]
	cmd.Args = cmd.Args[4:]
	cmd.ExtraFiles = append(cmd.ExtraFiles[:count], cmd.ExtraFiles[count+1:]...)
}

// relayedExecutable checks a program the broker will start. Inside the
// protected launch's module view the caller's programs sit on noexec mounts, so
// asking the kernel whether this process may execute them always answers no;
// the broker starts them outside, where the caller's own authority decides.
// Here a regular file with an execute bit is a program.
func relayedExecutable(path string) (string, error) {
	info, err := os.Stat(path) // #nosec G703 -- the caller's program path; only its mode is read before the broker starts it outside the module view
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", &os.PathError{Op: "lookpath", Path: path, Err: fs.ErrPermission}
	}
	return path, nil
}

// resolveRelayedProgram finds the bare program name os/exec could not resolve
// inside the module view, on the PATH os/exec searched.
func resolveRelayedProgram(cmd *exec.Cmd, state protectedLaunch) {
	if !state.protected || cmd.Err == nil || !errors.Is(cmd.Err, exec.ErrNotFound) || strings.ContainsRune(cmd.Path, '/') {
		return
	}
	if found, err := LookPathIn(cmd.Path, os.Getenv("PATH")); err == nil {
		cmd.Path, cmd.Err = found, nil
	}
}

// relayedCommand reports a command that is the broker relay.
func relayedCommand(cmd *exec.Cmd) bool {
	return cmd != nil && len(cmd.Args) > 1 && cmd.Args[1] == relayCommand
}
