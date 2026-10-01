package confine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Privileged host effects remain outside the applied boundary.
func TestSeatbeltHostCapabilityBoundary(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	type probe struct {
		capability string
		bin        string
		args       []string
		// deniedSubstring provides a diagnostic match.
		deniedSubstring string
	}

	probes := []probe{
		// Raw device writes.
		{"dd", "/bin/dd", []string{"if=/dev/zero", "of=/dev/disk0", "bs=1", "count=1"}, "not permitted"},
		{"dd", "/bin/dd", []string{"if=/dev/zero", "of=/dev/rdisk0", "bs=1", "count=1"}, "not permitted"},
		// Partition and filesystem effects.
		{"mkfs*", "/sbin/newfs_hfs", []string{"-N", "/dev/disk0"}, "not permitted"},
		{"fdisk", "/usr/sbin/fdisk", []string{"/dev/disk0"}, "not permitted"},
		{"sfdisk", "/sbin/sfdisk", []string{"/dev/disk0"}, "not permitted"},
		{"parted", "/usr/local/bin/parted", []string{"/dev/disk0", "print"}, "not permitted"},
		// Power control.
		{"reboot", "/sbin/reboot", nil, "not permitted"},
		{"halt", "/sbin/halt", nil, "not permitted"},
		{"shutdown", "/sbin/shutdown", []string{"-h", "now"}, "not"}, // "NOT super-user" or seatbelt
		{"poweroff", "/sbin/poweroff", nil, "not permitted"},
		// Privilege escalation.
		{"sudo", "/usr/bin/sudo", []string{"-n", "id"}, "not permitted"},
		{"su", "/usr/bin/su", []string{"-c", "id"}, "not permitted"},
		{"doas", "doas", []string{"id"}, "not permitted"},
	}

	seen := map[string]bool{}
	for _, p := range probes {
		t.Run(p.capability+"/"+filepath.Base(p.bin), func(t *testing.T) {
			bin := p.bin
			if !filepath.IsAbs(bin) {
				path, err := exec.LookPath(bin)
				if err != nil {
					t.Skipf("%s not on PATH — class covered by device/setuid peers", bin)
					return
				}
				bin = path
			}
			if _, err := os.Stat(bin); err != nil {
				t.Skipf("%s absent — class covered by peer probes", bin)
				return
			}
			code, out := confinedRun(t, self, c, bin, p.args...)
			if code == 0 {
				t.Fatalf("%s must be denied under Seatbelt; exit=0 out=%q", p.capability, out)
			}
			if p.deniedSubstring != "" && !strings.Contains(strings.ToLower(out), strings.ToLower(p.deniedSubstring)) &&
				!strings.Contains(strings.ToLower(out), "operation not permitted") &&
				!strings.Contains(strings.ToLower(out), "approval denied") {
				// Non-zero exit is the hard requirement; substring is diagnostic.
				t.Logf("denied with exit=%d out=%q (substring %q optional)", code, out, p.deniedSubstring)
			}
			seen[p.capability] = true
		})
	}

	// Positive controls keep ordinary writes usable.
	if code, out := confinedRun(t, self, c, "/bin/dd", "if=/dev/zero", "of="+filepath.Join(proj, "ok.bin"), "bs=1", "count=1"); code != 0 {
		t.Fatalf("in-project dd must succeed under Seatbelt: exit=%d out=%q", code, out)
	}
	if code, out := confinedRun(t, self, c, "/bin/dd", "if=/dev/zero", "of=/dev/null", "bs=1", "count=1"); code != 0 {
		t.Fatalf("dd to /dev/null must succeed: exit=%d out=%q", code, out)
	}

	for _, must := range []string{"dd", "sudo", "reboot"} {
		if !seen[must] {
			t.Fatalf("required probe %q did not run — host-capability boundary unproven", must)
		}
	}
}

// The profile explicitly denies privileged host surfaces.
func TestBuildProfileDeniesHostCapabilitySurfaces(t *testing.T) {
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	for _, want := range []string{
		`(deny file-write*`,
		`/dev/disk`,
		`/dev/rdisk`,
		`(deny mach-lookup`,
		`com.apple.system.powerd`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("BuildProfile missing host-capability denial %q:\n%s", want, p)
		}
	}
}
