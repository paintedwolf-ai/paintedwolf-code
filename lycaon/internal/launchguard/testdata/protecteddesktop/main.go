//go:build linux

// protecteddesktop stands in for the desktop image in protected launch tests.
// It reports the kernel facts it was started with, deliberately stays
// dumpable so only the namespace boundary can refuse an outside reader, and
// waits for its standard input to close.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

// Facts mirrors the test's decoding of this report.
type Facts struct {
	PID             int                         `json:"pid"`
	UIDs            [3]int                      `json:"uids"`
	GIDs            [3]int                      `json:"gids"`
	Groups          []int                       `json:"groups"`
	Namespace       launchverify.NamespaceFacts `json:"namespace"`
	Executable      string                      `json:"executable"`
	Arguments       []string                    `json:"arguments"`
	Environment     []string                    `json:"environment_keys"`
	BrokerPeerPID   int                         `json:"broker_peer_pid"`
	BrokerPeerUID   int                         `json:"broker_peer_uid"`
	BrokerType      int                         `json:"broker_type"`
	ProtectedLaunch string                      `json:"protected_launch"`
	GenerationFrame json.RawMessage             `json:"generation_frame"`
	LifetimeRegular bool                        `json:"lifetime_regular"`
	OpenBeyond      []string                    `json:"open_beyond"`
	InstallWrite    string                      `json:"install_write"`
	Remount         string                      `json:"remount"`
	ReadError       string                      `json:"read_error,omitempty"`
	BrokerOutput    string                      `json:"broker_output,omitempty"`
	BrokerExit      int                         `json:"broker_exit"`
	BrokerError     string                      `json:"broker_error,omitempty"`
	SupervisorState string                      `json:"supervisor_state,omitempty"`
	BrokerAgents    int                         `json:"broker_agents"`
	AbandonedEnded  bool                        `json:"abandoned_ended"`
	DetachedPID     int                         `json:"detached_pid,omitempty"`
	ExecOutside     string                      `json:"exec_outside"`
	ExecGeneration  string                      `json:"exec_generation"`
	Exec            *ExecProbe                  `json:"exec,omitempty"`
	ModuleUser      string                      `json:"module_user"`
	ModuleLibrary   string                      `json:"module_library"`
	BrokerUserExec  string                      `json:"broker_user_exec,omitempty"`
}

type vaultCommand struct {
	Socket  string                `json:"socket"`
	Request vaultidentity.Request `json:"request"`
}

// desktopBroker owns descriptor 6 for the lifetime of the protected desktop so
// a GC finalizer cannot close the broker socket while the desktop is running.
var desktopBroker *os.File

func main() {
	if len(os.Args) == 5 && os.Args[1] == "--browser-engine-victim" {
		os.Exit(runBrowserEngineVictim(os.Args[2], os.Args[3], os.Args[4]))
	}
	if len(os.Args) == 3 && os.Args[1] == "--engine-vault" {
		runEngineVault(os.Args[2])
		return
	}
	// Consume the same sealed startup frame the desktop claims before threads.
	frame := os.NewFile(4, "selected-generation")
	frameRaw, _ := io.ReadAll(io.NewSectionReader(frame, 0, 128<<10))
	_ = frame.Close()
	desktopBroker = os.NewFile(6, "broker")
	ready := make(chan struct{}, 4)
	for range 4 {
		go func() {
			runtime.LockOSThread()
			ready <- struct{}{}
			select {}
		}()
	}
	for range 4 {
		<-ready
	}
	var facts Facts
	facts.GenerationFrame = frameRaw
	facts.ProtectedLaunch = os.Getenv("PW_PROTECTED_LAUNCH")
	facts.PID = os.Getpid()
	facts.UIDs[0], facts.UIDs[1], facts.UIDs[2] = unix.Getresuid()
	facts.GIDs[0], facts.GIDs[1], facts.GIDs[2] = unix.Getresgid()
	facts.Groups, _ = unix.Getgroups()
	ns, err := launchverify.ReadNamespace("/proc", os.Getpid())
	if err != nil {
		facts.ReadError = err.Error()
	}
	facts.Namespace = ns
	facts.Executable, _ = os.Readlink("/proc/self/exe")
	facts.Arguments = os.Args
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		facts.Environment = append(facts.Environment, key)
	}
	sort.Strings(facts.Environment)
	if peer, err := unix.GetsockoptUcred(6, unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
		facts.BrokerPeerPID, facts.BrokerPeerUID = int(peer.Pid), int(peer.Uid)
	}
	facts.BrokerType, _ = unix.GetsockoptInt(6, unix.SOL_SOCKET, unix.SO_TYPE)
	var lifetime unix.Stat_t
	facts.LifetimeRegular = unix.Fstat(7, &lifetime) == nil && lifetime.Mode&unix.S_IFMT == unix.S_IFREG
	// Descriptors beyond the handed-off set, named by their /proc link. The Go
	// runtime opens its poller, wake eventfd and cgroup CPU limit file at startup
	// on the lowest free numbers, which reach 8 once the frame holds 4.
	for fd := 8; fd < 64; fd++ {
		target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		if err != nil || runtimeDescriptor(target) {
			continue
		}
		facts.OpenBeyond = append(facts.OpenBeyond, strconv.Itoa(fd)+" "+target)
	}
	install := filepath.Dir(filepath.Dir(facts.Executable))
	facts.ExecOutside = startName("/bin/true")
	facts.ExecGeneration = startName(filepath.Join(install, "bin", "pw-launch-entry"))
	facts.InstallWrite = errnoName(os.WriteFile(filepath.Join(install, "planted"), []byte("x"), 0o644))
	facts.Remount = errnoName(unix.Mount("", install, "", unix.MS_REMOUNT|unix.MS_BIND, ""))
	facts.ModuleUser, facts.ModuleLibrary = moduleProbe()
	for _, arg := range os.Args[1:] {
		if arg == "--broker-probe" {
			brokerProbe(&facts)
		}
		if arg == "--exec-probe" {
			scratch, err := os.MkdirTemp("", "pw-protected-exec-")
			if err == nil {
				probe := execProbe(scratch)
				facts.Exec = &probe
				_ = os.RemoveAll(scratch)
			}
		}
	}
	// Stay dumpable and traceable by anyone: only the namespace can refuse.
	_ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0)
	_ = unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0)
	if err := json.NewEncoder(os.Stdout).Encode(facts); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		out, err := exec.Command(filepath.Join(install, "bin", "pw"), "--engine-vault", line).Output()
		if err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Message: err.Error()})
			continue
		}
		_, _ = os.Stdout.Write(out)
	}
	runtime.KeepAlive(desktopBroker)
}

func runEngineVault(raw string) {
	var cmd vaultCommand
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Message: err.Error()})
		return
	}
	client := vaultidentity.NewClient(cmd.Socket)
	defer func() { _ = client.Close() }()
	resp, err := client.Do(cmd.Request)
	if err != nil && resp.Status == "" {
		resp = vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Message: err.Error()}
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}

// brokerProbe runs one program through the supervisor's broker and reports
// where it ran, abandons a second one and reports whether it ended, and
// starts a detached background child that must outlive the launch without
// holding the supervisor or generation lock open.
func brokerProbe(facts *Facts) {
	broker := desktopBroker
	// What the broker's peer and its agent look like when the first request
	// goes out, so a refused request names the process that was missing.
	facts.SupervisorState = processState(facts.BrokerPeerPID)
	facts.BrokerAgents = brokerAgentCount()
	null, err := os.Open("/dev/null")
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	defer func() { _ = null.Close() }()
	read, write, err := os.Pipe()
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	channel, err := launchbroker.Send(broker, launchbroker.Request{
		Argv:        []string{"sh", "-c", "readlink /proc/self/ns/user; grep NoNewPrivs /proc/self/status"},
		Environment: []string{"PATH=/usr/bin:/bin"}, Dir: "/",
	}, null, write, write)
	_ = write.Close()
	if err != nil {
		facts.BrokerError = "send: " + err.Error()
		return
	}
	output, _ := io.ReadAll(read)
	_ = read.Close()
	facts.BrokerOutput = string(output)
	for {
		event, err := launchbroker.ReadEvent(channel)
		if err != nil {
			facts.BrokerError = err.Error()
			break
		}
		if event.Error != "" {
			facts.BrokerError = event.Error
			break
		}
		if event.Exited {
			facts.BrokerExit = event.Code
			break
		}
	}
	_ = channel.Close()
	facts.BrokerUserExec = brokeredUserProgram(broker, null)
	sleeper, err := launchbroker.Send(broker, launchbroker.Request{
		Argv: []string{"sleep", "60"}, Environment: []string{"PATH=/usr/bin:/bin"}, Dir: "/",
	}, null, null, null)
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	started, err := launchbroker.ReadEvent(sleeper)
	_ = sleeper.Close()
	if err != nil || started.Started <= 0 {
		facts.BrokerError = "abandoned call never started"
		return
	}
	for i := 0; i < 250; i++ {
		if _, err := os.Stat("/proc/" + strconv.Itoa(started.Started)); err != nil {
			facts.AbandonedEnded = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !facts.AbandonedEnded {
		return
	}
	detachedRead, detachedWrite, err := os.Pipe()
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	detachedCall, err := launchbroker.Send(broker, launchbroker.Request{
		Argv:        []string{"sh", "-c", "sleep 60 </dev/null >/dev/null 2>&1 & echo $!"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
		Detach:      true,
	}, null, detachedWrite, null)
	_ = detachedWrite.Close()
	if err != nil {
		_ = detachedRead.Close()
		facts.BrokerError = err.Error()
		return
	}
	rawPID, _ := io.ReadAll(detachedRead)
	_ = detachedRead.Close()
	_ = detachedCall.Close()
	facts.DetachedPID, _ = strconv.Atoi(strings.TrimSpace(string(rawPID)))
}

// moduleProbe maps a file of the caller's own, and one from the fixture's
// library root, executable, the way the dynamic loader maps a module.
func moduleProbe() (user, library string) {
	planted := filepath.Join(os.Getenv("HOME"), "planted.so")
	if err := os.WriteFile(planted, []byte("not a module"), 0o755); err != nil {
		return err.Error(), ""
	}
	user = mapExecutable(planted)
	library = "no regular file in /usr/lib"
	entries, _ := os.ReadDir("/usr/lib")
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return user, mapExecutable(filepath.Join("/usr/lib", entry.Name()))
		}
	}
	return user, library
}

func mapExecutable(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = file.Close() }()
	region, err := unix.Mmap(int(file.Fd()), 0, 1, unix.PROT_READ|unix.PROT_EXEC, unix.MAP_PRIVATE)
	if err != nil {
		return errnoName(err)
	}
	_ = unix.Munmap(region)
	return "ok"
}

// brokeredUserProgram runs a program from the caller's own files through the
// broker, which executes outside the module view; its output is its result.
func brokeredUserProgram(broker, null *os.File) string {
	script := filepath.Join(os.Getenv("HOME"), "brokered.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho brokered-user-program\n"), 0o755); err != nil {
		return err.Error()
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err.Error()
	}
	channel, err := launchbroker.Send(broker, launchbroker.Request{
		Argv: []string{script}, Environment: []string{"PATH=/usr/bin:/bin"}, Dir: "/",
	}, null, write, write)
	_ = write.Close()
	if err != nil {
		return err.Error()
	}
	defer func() { _ = channel.Close() }()
	output, _ := io.ReadAll(read)
	for {
		event, err := launchbroker.ReadEvent(channel)
		if err != nil {
			return err.Error()
		}
		if event.Error != "" {
			return event.Error
		}
		if event.Exited {
			return strings.TrimSpace(string(output))
		}
	}
}

// processState reports the kernel's State line for pid, or why it is unreadable.
func processState(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return err.Error()
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if state, ok := strings.CutPrefix(line, "State:"); ok {
			return strings.TrimSpace(state)
		}
	}
	return "unknown"
}

// brokerAgentCount counts the broker agent processes visible in /proc.
func brokerAgentCount() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err == nil && strings.Contains(string(raw), "--broker-agent") {
			count++
		}
	}
	return count
}

// startName reports whether the kernel let this process execute path.
func startName(path string) string {
	pid, err := syscall.ForkExec(path, []string{path}, &syscall.ProcAttr{Files: []uintptr{0, 1, 2}})
	if err != nil {
		return errnoName(err)
	}
	var status unix.WaitStatus
	_, _ = unix.Wait4(pid, &status, 0, nil)
	return "started"
}

func errnoName(err error) string {
	var errno unix.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &errno):
		return unix.ErrnoName(errno)
	default:
		return err.Error()
	}
}

// runtimeDescriptor reports a descriptor the Go runtime holds for itself.
func runtimeDescriptor(target string) bool {
	return target == "anon_inode:[eventpoll]" || target == "anon_inode:[eventfd]" || strings.HasPrefix(target, "/sys/fs/cgroup/")
}
