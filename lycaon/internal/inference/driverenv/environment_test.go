package driverenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConfigurationRequiresBoundedExactShape(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"format_version":2,"mesa_icds":["/nix/driver.json"]}`,
		`{"format_version":1,"mesa_icds":[]}`, `{"format_version":1,"mesa_icds":["relative"]}`,
		`{"format_version":1,"mesa_icds":["/nix/../driver.json"]}`,
		`{"format_version":1,"mesa_icds":["/nix/a:/nix/b"]}`,
		`{"format_version":1,"mesa_icds":["/nix/a","/nix/a"]}`,
		`{"format_version":1,"mesa_icds":["/nix/a"],"unknown":true}`,
		`{"format_version":1,"mesa_icds":["/nix/a"]} {}`,
		strings.Repeat(" ", 65537),
	} {
		t.Run(body[:min(len(body), 80)], func(t *testing.T) {
			root := configFixture(t, body)
			if config, err := readConfiguration(root); err == nil || config != nil {
				t.Fatalf("accepted invalid configuration: %+v, %v", config, err)
			}
		})
	}
	root := configFixture(t, `{"format_version":1,"mesa_icds":["/nix/a","/nix/b"]}`)
	config, err := readConfiguration(root)
	testutil.FailErr(t, "read valid driver configuration", err)
	if !slices.Equal(config.MesaICDs, []string{"/nix/a", "/nix/b"}) {
		t.Fatalf("ICDs = %v", config.MesaICDs)
	}
	for _, root := range []string{"", t.TempDir()} {
		config, err := readConfiguration(root)
		testutil.FailErr(t, "absent configuration", err)
		if config != nil {
			t.Fatalf("absent configuration = %+v", config)
		}
	}
}

func configFixture(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "create generation directory", os.Mkdir(filepath.Join(root, "generation"), 0o700))
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "generation", resourceName), []byte(body), 0o600))
	return root
}

func TestVendorEnvironmentHasPrivateICDAndExplicitLifetime(t *testing.T) {
	config := configuration{FormatVersion: 1, MesaICDs: []string{"/nix/mesa.json"}}
	libraries := []string{"/usr/lib/libnvidia-glsi.so.580.178.04", "/usr/lib/libEGL_nvidia.so.580.178.04"}
	values, cleanup, err := environment(config, libraries, "1.4.312")
	testutil.FailErr(t, "prepare vendor environment", err)
	defer cleanup()
	if len(values) != 2 || values[1] != "LD_PRELOAD="+strings.Join(libraries, ":") {
		t.Fatalf("environment = %v", values)
	}
	prefix := "VK_DRIVER_FILES=/nix/mesa.json:"
	if !strings.HasPrefix(values[0], prefix) {
		t.Fatalf("Mesa selection lost: %v", values)
	}
	path := strings.TrimPrefix(values[0], prefix)
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read active ICD", err)
	var manifest struct {
		ICD struct {
			Library string `json:"library_path"`
			API     string `json:"api_version"`
		} `json:"ICD"`
	}
	testutil.FailErr(t, "decode ICD", json.Unmarshal(data, &manifest))
	if manifest.ICD.Library != libraries[1] || manifest.ICD.API != "1.4.312" {
		t.Fatalf("ICD selected %q", manifest.ICD.Library)
	}
	info, err := os.Stat(filepath.Dir(path))
	testutil.FailErr(t, "read private directory", err)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions = %v", info.Mode())
	}
	cleanup()
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("driver directory survived cleanup: %v", err)
	}
	values, cleanup, err = environment(config, nil, "")
	testutil.FailErr(t, "prepare Mesa environment", err)
	cleanup()
	if !slices.Equal(values, []string{"VK_DRIVER_FILES=/nix/mesa.json"}) {
		t.Fatalf("Mesa environment = %v", values)
	}
}

func TestVendorPreparationFailureReturnsSafeCleanup(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	values, cleanup, err := environment(configuration{MesaICDs: []string{"/nix/a"}}, []string{"/usr/lib/driver"}, "1.4.312")
	if err == nil || len(values) != 0 {
		t.Fatalf("preparation = %v, %v", values, err)
	}
	cleanup()
}
