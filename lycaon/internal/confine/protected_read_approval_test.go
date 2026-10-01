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
	}{
		{name: "unapproved"},
		{name: "certificate", grant: certificate, certificateAllowed: true},
		{name: "credential", grant: credential, credentialAllowed: true},
		{name: "directory", grant: parent, certificateAllowed: true, credentialAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boundary := confine.Confinement{Roots: []string{t.TempDir()}, ReadDenyPaths: []string{data}}
			if tc.grant != "" {
				boundary.ProtectedReadGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(tc.grant)}
			}
			for _, probe := range []struct {
				path    string
				allowed bool
			}{
				{certificate, tc.certificateAllowed}, {credential, tc.credentialAllowed}, {control, false},
			} {
				code, output := confinedRun(t, self, boundary, "/bin/cat", probe.path)
				if (code == 0) != probe.allowed {
					t.Fatalf("read %s exit=%d allowed=%v output=%s", probe.path, code, probe.allowed, output)
				}
			}
		})
	}
}
