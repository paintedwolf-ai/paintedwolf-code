package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSeatbeltProtectedReadApprovalScope(t *testing.T) {
	self := requireSeatbelt(t)
	parent := t.TempDir()
	configDir := filepath.Join(parent, "control")
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	data := filepath.Join(parent, "data")
	certificate := filepath.Join(data, "cacert.pem")
	credential := filepath.Join(data, "credentials")
	control := filepath.Join(configDir, "api.token")
	for _, path := range []string{certificate, credential, control} {
		testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write synthetic protected fixture", os.WriteFile(path, []byte("synthetic fixture\n"), 0o600))
	}
	for _, tc := range []struct {
		name               string
		grant              string
		certificateAllowed bool
		credentialAllowed  bool
		dirStatAllowed     bool
		dirListAllowed     bool
	}{
		{name: "unapproved"},
		{name: "certificate", grant: certificate, certificateAllowed: true, dirStatAllowed: true},
		{name: "credential", grant: credential, credentialAllowed: true, dirStatAllowed: true},
		{name: "directory", grant: parent, certificateAllowed: true, credentialAllowed: true, dirStatAllowed: true, dirListAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boundary := confine.Confinement{Roots: []string{t.TempDir()}, ReadDenyPaths: []string{data}}
			if tc.grant != "" {
				boundary.ProtectedReadGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(tc.grant)}
			}
			for _, probe := range []struct {
				prog    string
				path    string
				allowed bool
			}{
				{"/bin/cat", certificate, tc.certificateAllowed},
				{"/bin/cat", credential, tc.credentialAllowed},
				{"/bin/cat", control, false},
				{"/usr/bin/stat", data, tc.dirStatAllowed},
				{"/bin/ls", data, tc.dirListAllowed},
			} {
				code, output := confinedRun(t, self, boundary, probe.prog, probe.path)
				if (code == 0) != probe.allowed {
					t.Fatalf("%s %s exit=%d allowed=%v output=%s", probe.prog, probe.path, code, probe.allowed, output)
				}
			}
		})
	}
}
