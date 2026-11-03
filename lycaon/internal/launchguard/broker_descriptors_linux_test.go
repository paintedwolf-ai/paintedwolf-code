//go:build linux

package launchguard

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrokerPreservesEverySparseDescriptorLayoutInTheChild(t *testing.T) {
	_, client, _ := startTestBrokerAgent(t)
	null, err := os.Open(os.DevNull)
	testutil.FailErr(t, "open transport filler", err)
	defer null.Close()
	for mask := range 8 {
		layout := fmt.Sprintf("%03b", mask)
		t.Run(layout, func(t *testing.T) {
			read, write, err := os.Pipe()
			testutil.FailErr(t, "create child observation pipe", err)
			defer read.Close()
			defer write.Close()
			testutil.FailErr(t, "bound descriptor observation", read.SetReadDeadline(time.Now().Add(5*time.Second)))
			request := launchbroker.Request{
				Argv:        []string{"sh", "-c", `for fd in 3 4 5; do if [ -e "/proc/$$/fd/$fd" ]; then printf 1; else printf 0; fi; done`},
				Environment: []string{"PATH=/usr/bin:/bin"},
				Dir:         "/", ExtraFileLayout: layout,
			}
			channel, err := launchbroker.Send(client, request, null, write, null, null, null, null)
			testutil.FailErr(t, "send sparse descriptor request", err)
			defer channel.Close()
			testutil.FailErr(t, "release parent observation writer", write.Close())
			started, err := launchbroker.ReadEvent(channel)
			testutil.FailErr(t, "observe actual child start", err)
			if started.Started <= 0 || started.Error != "" {
				t.Fatalf("child start: %+v", started)
			}
			output, err := io.ReadAll(read)
			testutil.FailErr(t, "read actual inherited descriptor layout", err)
			exited, err := launchbroker.ReadEvent(channel)
			testutil.FailErr(t, "observe actual child exit", err)
			if !exited.Exited || exited.Code != 0 || exited.Error != "" || string(output) != layout {
				t.Fatalf("layout %q: child observed %q, exit %+v", layout, output, exited)
			}
		})
	}
}
