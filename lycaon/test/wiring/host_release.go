package wiring

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/session"
)

// latestHostAllowance bounds the closed hosts that process-wide hooks keeping
// only the most recent host may still reference after a suite. Retention
// that grows with the number of tests exceeds it.
const latestHostAllowance = 4

var builtHosts struct {
	sync.Mutex
	hosts []builtHost
}

type builtHost struct {
	test    string
	manager weak.Pointer[session.Manager]
}

func trackHost(test string, sa *app.ServeApp) {
	builtHosts.Lock()
	defer builtHosts.Unlock()
	builtHosts.hosts = append(builtHosts.hosts, builtHost{test: test, manager: weak.Make(sa.SessionMgr)})
}

// RunReleasingHosts runs a package's tests and then fails the package when
// the hosts they closed stay reachable. Each retained host holds tens of
// megabytes, so a suite that leaks them grows until it exhausts the runner.
func RunReleasingHosts(m *testing.M) int {
	code := m.Run()
	if code != 0 {
		return code
	}
	retained := retainedHosts(10 * time.Second)
	if len(retained) <= latestHostAllowance {
		return code
	}
	builtHosts.Lock()
	built := len(builtHosts.hosts)
	builtHosts.Unlock()
	fmt.Fprintf(os.Stderr, "FAIL: %d of %d closed test hosts are still reachable; a process-wide registry "+
		"(observer list, hook, timer, or cache) still references them. Return a release from the registration "+
		"and track it in the host's resources. Retained by: %s\n", len(retained), built, strings.Join(retained, ", "))
	return 1
}

func retainedHosts(wait time.Duration) []string {
	deadline := time.Now().Add(wait)
	for {
		runtime.GC()
		var retained []string
		builtHosts.Lock()
		for _, host := range builtHosts.hosts {
			if host.manager.Value() != nil {
				retained = append(retained, host.test)
			}
		}
		builtHosts.Unlock()
		if len(retained) <= latestHostAllowance || time.Now().After(deadline) {
			return retained
		}
		time.Sleep(100 * time.Millisecond)
	}
}
