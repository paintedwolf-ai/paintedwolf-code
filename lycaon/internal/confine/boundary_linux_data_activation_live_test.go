//go:build linux

package confine_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

const dataActivationMode = "PW_CONFINE_DATA_ACTIVATION"
const dataActivationName = "dev.paintedwolf.ConfinementFixture"
const dataActivationFlag = "-test.run=^TestLinuxDataActivationProbeProcess$"

func defaultDataBoundary(t *testing.T, custom bool) (string, *confine.Confinement) {
	t.Helper()
	self := requireLinuxSandbox(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "")
	home := outsideTemporaryWriteRoots(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_DIRS", filepath.Join(home, "system-data"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if custom {
		t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	}
	box, applied := confine.DefaultConfinement(confine.Request{Roots: sandboxRoots(t.TempDir()), Egress: confine.EgressDeny})
	if !applied || box == nil {
		t.Fatal("default Linux boundary was not admitted")
	}
	return self, box
}

func dataWriteRefused(errno string) bool {
	return errno == "EACCES" || errno == "EPERM" || errno == "EROFS"
}

func TestLinuxDefaultDataAndStateCannotPlantLaunchers(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(strconv.FormatBool(custom), func(t *testing.T) {
			_, box := defaultDataBoundary(t, custom)
			home := os.Getenv("HOME")
			data, state := filepath.Join(home, ".local/share"), filepath.Join(home, ".local/state")
			if custom {
				data, state = os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_STATE_HOME")
			}
			var steps []floorStep
			bases := []string{data, state}
			if custom {
				bases = append(bases, filepath.Join(home, ".local/share"), filepath.Join(home, ".local/state"))
			}
			for _, base := range bases {
				for _, relative := range []string{"dbus-1/services/fixture.service", "systemd/user/fixture.service", "applications/fixture.desktop"} {
					path := filepath.Join(base, relative)
					testutil.FailErr(t, "create launch-directory fixture", os.MkdirAll(filepath.Dir(path), 0o700))
					steps = append(steps, floorStep{"create " + path, "refused"})
				}
				link := filepath.Join(base, "systemd/user/default.target.wants/fixture.service")
				testutil.FailErr(t, "create wants-directory fixture", os.MkdirAll(filepath.Dir(link), 0o700))
				steps = append(steps, floorStep{"symlink ../fixture.service " + link, "refused"})
			}
			for _, relative := range []string{"pnpm/store", "NuGet/http-cache", "NuGet/plugin-cache"} {
				path := filepath.Join(data, relative)
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("first-use store already exists: %v", err)
				}
				steps = append(steps, floorStep{"create " + filepath.Join(path, "package"), "ok"})
			}
			script := make([]string, len(steps))
			for index, step := range steps {
				script[index] = step.line
			}
			results := floorsRun(t, *box, home, script...)
			for _, step := range steps {
				actual := results[step.line]
				if step.want == "refused" && dataWriteRefused(actual) {
					continue
				}
				if actual != step.want {
					t.Errorf("%s: got %q, want %q", step.line, actual, step.want)
				}
			}
			application := filepath.Join(data, "dev.paintedwolf.code", "host-state")
			testutil.FailErr(t, "create host-owned application data", os.MkdirAll(filepath.Dir(application), 0o700))
			testutil.FailErr(t, "engine writes its own application data outside confinement", os.WriteFile(application, []byte("host-owned"), 0o600))
		})
	}
}

func TestLinuxUnavailablePackageStoreDoesNotPreventPlainLaunch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("OS permission fixture requires an unprivileged test account")
	}
	for _, kind := range []string{"file-parent", "file-store", "inaccessible-parent"} {
		t.Run(kind, func(t *testing.T) {
			self, box := defaultDataBoundary(t, true)
			data := os.Getenv("XDG_DATA_HOME")
			parent := filepath.Join(data, "pnpm")
			store := filepath.Join(parent, "store")
			testutil.FailErr(t, "create data fixture", os.Mkdir(data, 0o700))
			switch kind {
			case "file-parent":
				testutil.FailErr(t, "create non-directory parent", os.WriteFile(parent, []byte("unchanged"), 0o600))
			case "file-store":
				testutil.FailErr(t, "create store parent", os.Mkdir(parent, 0o700))
				testutil.FailErr(t, "create non-directory store", os.WriteFile(store, []byte("unchanged"), 0o600))
			case "inaccessible-parent":
				testutil.FailErr(t, "create inaccessible parent", os.Mkdir(parent, 0))
				t.Cleanup(func() { testutil.FailErr(t, "restore owned fixture permissions", os.Chmod(parent, 0o700)) })
			}
			boundary := confine.BoundaryOf(box)
			if !boundary.Applied || boundary.Filesystem.Verdict(confine.AccessWrite, store).Allowed {
				t.Fatal("default boundary claimed an unavailable package directory")
			}
			status, output := linuxConfinedRun(t, self, *box, nil, requireTool(t, "true"))
			if status.ExitStatus() != 0 {
				t.Fatalf("optional package store prevented an unrelated launch: %s", output)
			}
			if kind != "inaccessible-parent" {
				path := parent
				if kind == "file-store" {
					path = store
				}
				raw, err := os.ReadFile(path)
				testutil.FailErr(t, "read unchanged fixture", err)
				if string(raw) != "unchanged" {
					t.Fatal("store preparation modified an unavailable directory")
				}
			}
		})
	}
}

// The positive control exits nonzero after writing, so the bus reports its
// completed process even though it deliberately never owns the service name.
func TestLinuxDataActivationProbeProcess(t *testing.T) {
	switch os.Getenv(dataActivationMode) {
	case "plant":
		marker := os.WriteFile(os.Getenv("PW_ACTIVATION_MARKER"), []byte("confined"), 0o600)
		service := os.WriteFile(os.Getenv("PW_ACTIVATION_SERVICE"), []byte(os.Getenv("PW_ACTIVATION_CONTENT")), 0o600)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"marker": floorsResult(unwrapErrno(marker)), "service": floorsResult(unwrapErrno(service)),
		})
		os.Exit(0)
	case "activate":
		namespace, err := os.Readlink("/proc/self/ns/net")
		testutil.FailErr(t, "read activated network namespace", err)
		raw, err := json.Marshal(map[string]string{"network_namespace": namespace})
		testutil.FailErr(t, "encode activated marker", err)
		testutil.FailErr(t, "write unconfined activation marker", os.WriteFile(os.Getenv("PW_ACTIVATION_MARKER"), raw, 0o600))
		os.Exit(7)
	}
}

func TestLinuxDefaultDataCannotEscapeThroughDBusActivation(t *testing.T) {
	self, box := defaultDataBoundary(t, true)
	home := os.Getenv("HOME")
	data := os.Getenv("XDG_DATA_HOME")
	service := filepath.Join(data, "dbus-1/services", dataActivationName+".service")
	marker := filepath.Join(home, "outside-marker")
	testutil.FailErr(t, "create private activation directory", os.MkdirAll(filepath.Dir(service), 0o700))
	content := "[D-BUS Service]\nName=" + dataActivationName + "\nExec=" + strconv.Quote(self) + " " + dataActivationFlag + "\n"
	environment := append(os.Environ(), "PW_ACTIVATION_MARKER="+marker, "PW_ACTIVATION_SERVICE="+service, "PW_ACTIVATION_CONTENT="+content)
	status, output := linuxConfinedRun(t, self, *box, append(slices.Clone(environment), dataActivationMode+"=plant"), self, dataActivationFlag)
	var refused map[string]string
	if status.ExitStatus() != 0 || json.Unmarshal([]byte(output), &refused) != nil ||
		!dataWriteRefused(refused["marker"]) || !dataWriteRefused(refused["service"]) {
		t.Fatalf("confined direct write and service planting were not refused: %s", output)
	}
	for _, path := range []string{service, marker} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused activation left an object: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	bus := exec.CommandContext(ctx, requireTool(t, "dbus-daemon"), "--session", "--nofork", "--nopidfile", "--print-address=1")
	bus.Env = append(slices.Clone(environment), dataActivationMode+"=activate")
	stdout, err := bus.StdoutPipe()
	testutil.FailErr(t, "open private bus address pipe", err)
	testutil.FailErr(t, "start private activation bus", bus.Start())
	t.Cleanup(func() { _ = bus.Process.Kill(); _ = bus.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	testutil.FailErr(t, "read private bus address", err)
	selected, err := linuxbus.SelectSession(func(key string) (string, bool) {
		return strings.TrimSpace(address), key == "DBUS_SESSION_BUS_ADDRESS"
	}, os.Getuid())
	testutil.FailErr(t, "select private activation bus", err)
	client, err := selected.Connect(ctx)
	testutil.FailErr(t, "connect private activation client", err)
	defer func() { _ = client.Close() }()
	call := client.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.StartServiceByName", 0, dataActivationName, uint32(0))
	var absent dbus.Error
	if !errors.As(call.Err, &absent) || absent.Name != "org.freedesktop.DBus.Error.ServiceUnknown" {
		t.Fatalf("bus activated a service the boundary refused to plant: %v", call.Err)
	}
	testutil.FailErr(t, "write unconfined positive-control service", os.WriteFile(service, []byte(content), 0o600))
	testutil.FailErr(t, "reload private bus service directory", client.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ReloadConfig", 0).Err)
	call = client.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.StartServiceByName", 0, dataActivationName, uint32(0))
	var exited dbus.Error
	if !errors.As(call.Err, &exited) || exited.Name != "org.freedesktop.DBus.Error.Spawn.ChildExited" {
		t.Fatalf("positive-control process did not complete its activation: %v", call.Err)
	}
	raw, err := os.ReadFile(marker)
	testutil.FailErr(t, "read positive-control marker", err)
	var activated map[string]string
	testutil.FailErr(t, "decode positive-control marker", json.Unmarshal(raw, &activated))
	namespace, err := os.Readlink("/proc/self/ns/net")
	testutil.FailErr(t, "read host network namespace", err)
	if activated["network_namespace"] != namespace {
		t.Fatal("activation positive control did not run in the host network namespace")
	}
}
