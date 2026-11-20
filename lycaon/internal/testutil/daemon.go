package testutil

import (
	"fmt"
	"strconv"
)

// DetachedDaemonScript is a python3 program that leaves its process tree the
// way a real daemon does: a new session, SIGTERM ignored, standard streams
// closed. The launcher reaps the middle process, so only the daemon remains in
// its session. The grandchild writes its pid to pidPath; it lives until SIGKILL.
// With stay the launcher waits for the pid file and keeps running; without it
// the launcher exits once the daemon is up.
func DetachedDaemonScript(pidPath string, stay bool) string {
	launcher := ""
	if stay {
		launcher = "while True:\n    signal.pause()\n"
	}
	return fmt.Sprintf(`import os, signal, time
path = %s
middle = os.fork()
if middle == 0:
    os.setsid()
    if os.fork() == 0:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        os.closerange(0, 256)
        with open(path + '.tmp', 'w') as file:
            file.write(str(os.getpid()))
        os.rename(path + '.tmp', path)
        while True:
            signal.pause()
    os._exit(0)
os.waitpid(middle, 0)
while not os.path.exists(path):
    time.sleep(0.01)
%s`, strconv.Quote(pidPath), launcher)
}
