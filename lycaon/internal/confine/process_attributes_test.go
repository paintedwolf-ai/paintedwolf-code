//go:build unix

package confine_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The helper sets the requested limits and an ignored SIGHUP in itself just
// before it becomes the target, so the target starts with them.
func TestHelperAppliesProcessAttributesToTheTarget(t *testing.T) {
	nofile, err := confine.ReadResourceLimit("nofile")
	testutil.FailErr(t, "read descriptor limit", err)
	if nofile.Soft > nofile.Hard || nofile.Soft < 64 {
		t.Fatalf("descriptor limit %+v", nofile)
	}
	c, ok := confine.DefaultConfinement(confine.Request{HostExecution: true, Roots: []string{t.TempDir()}})
	if !ok || c == nil {
		t.Fatal("host execution not selected")
	}
	self, err := os.Executable()
	testutil.FailErr(t, "helper binary", err)
	probe := []string{"-c", `ulimit -n; kill -HUP $$; echo survived`}
	for _, tc := range []struct {
		name       string
		attributes confine.ProcessAttributes
		want       string
	}{
		{"limit and ignored hangup", confine.ProcessAttributes{Limits: []confine.ResourceLimit{{Resource: "nofile", Soft: 64, Hard: nofile.Hard}}, IgnoreHangup: true}, "64\nsurvived\n"},
		{"limit only", confine.ProcessAttributes{Limits: []confine.ResourceLimit{{Resource: "nofile", Soft: 48, Hard: nofile.Hard}}}, "48\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boundary := *c
			boundary.Process = tc.attributes
			command, cleanup, err := confine.Command(t.Context(), self, "/bin/sh", probe, boundary)
			testutil.FailErr(t, "build command", err)
			defer cleanup()
			if !strings.Contains(strings.Join(command.Args, " "), "--resource-limit nofile="+strconv.FormatUint(tc.attributes.Limits[0].Soft, 10)) {
				t.Fatalf("helper argv lacks the limit: %v", command.Args)
			}
			out, _ := command.Output()
			if string(out) != tc.want {
				t.Fatalf("target output %q, want %q", out, tc.want)
			}
		})
	}
}

func TestReadResourceLimitRefusesUnknownResources(t *testing.T) {
	for resource := range map[string]bool{"core": true, "fsize": true, "stack": true, "cpu": true, "nproc": true, "as": true} {
		if limit, err := confine.ReadResourceLimit(resource); err != nil || limit.Soft > limit.Hard {
			t.Errorf("%s: %+v, %v", resource, limit, err)
		}
	}
	if _, err := confine.ReadResourceLimit("rss"); err == nil {
		t.Fatal("read an unsupported resource")
	}
}
