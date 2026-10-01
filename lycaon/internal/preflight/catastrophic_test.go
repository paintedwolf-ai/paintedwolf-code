package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
)

func testFacts() HostFacts {
	return HostFacts{
		AppVersion:    "0.4.2",
		Build:         "abc123",
		SchemaVersion: 7,
		OSName:        "darwin",
		OSVersion:     "12.7",
		OSFloor:       "13.0",
		Arch:          "arm64",
	}
}

func TestBuildCatastrophicDetailQuotesAllowlistedFacts(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 3, 11, 0, time.UTC)
	res := Result{
		ID:     "os_version",
		Status: StatusBlocked,
		Code:   CodeOSBelowFloor,
		Detail: map[string]string{
			"reason":     ReasonOSBelowFloor,
			"want":       "13.0",
			"got":        "12.7",
			"resolution": "below_floor",
		},
	}
	got := BuildCatastrophicDetail(res, testFacts(), now)
	if got == nil {
		t.Fatal("BuildCatastrophicDetail returned nil for a coded result")
	}
	if got.Code != CodeOSBelowFloor || got.ProbeID != "os_version" {
		t.Fatalf("code/probe = %q/%q", got.Code, got.ProbeID)
	}
	if got.Resolution != "below_floor" {
		t.Fatalf("resolution = %q", got.Resolution)
	}
	if got.AppVersion != "0.4.2" || got.SchemaVersion != 7 || got.Arch != "arm64" {
		t.Fatalf("host facts not carried: %#v", got)
	}
	if got.OSFloor != "13.0" || got.OSVersion != "12.7" {
		t.Fatalf("os facts = %q / %q", got.OSFloor, got.OSVersion)
	}
	if got.ObservedAt != "2026-07-29T14:03:11Z" {
		t.Fatalf("observed_at = %q", got.ObservedAt)
	}
	if got.ConfigDirLabel != configdir.Label() {
		t.Fatalf("config dir label = %q", got.ConfigDirLabel)
	}
	// resolution is promoted to its own field, so repeating it as a fact would
	// read as if the probe emitted it twice.
	if _, dup := got.Facts["resolution"]; dup {
		t.Fatalf("facts should not repeat resolution: %#v", got.Facts)
	}
	if got.Facts["want"] != "13.0" || got.Facts["got"] != "12.7" {
		t.Fatalf("probe facts not passed through: %#v", got.Facts)
	}
}

// An unstamped local build reports no revision rather than an empty one.
func TestBuildCatastrophicDetailOmitsUnstampedBuild(t *testing.T) {
	facts := testFacts()
	facts.Build = ""
	got := BuildCatastrophicDetail(Result{ID: "os_version", Code: CodeOSBelowFloor}, facts, time.Now())
	if got.Build != "" {
		t.Fatalf("build = %q want empty", got.Build)
	}
}

func TestBuildCatastrophicDetailNilWithoutCode(t *testing.T) {
	if got := BuildCatastrophicDetail(Result{ID: "x", Status: StatusOK}, testFacts(), time.Now()); got != nil {
		t.Fatalf("an ok result has nothing to report: %#v", got)
	}
}

// The stop screen is the payload most likely to be pasted somewhere public, so
// no absolute path or home directory may reach it even if a probe forgot.
func TestBuildCatastrophicDetailRedactsPathsAndHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	res := Result{
		ID:   "config_dir",
		Code: CodeConfigDirUnwritable,
		Detail: map[string]string{
			"config_path": filepath.Join(home, ".config", "paintedwolf"),
			"engine_root": "/opt/definitely/absolute",
		},
	}
	got := BuildCatastrophicDetail(res, testFacts(), time.Now())
	for key, value := range got.Facts {
		if strings.Contains(value, home) {
			t.Fatalf("fact %q leaks the home directory: %q", key, value)
		}
		if strings.HasPrefix(value, "/") {
			t.Fatalf("fact %q is an absolute path: %q", key, value)
		}
	}
	if want := "~/.config/paintedwolf"; got.Facts["config_path"] != want {
		t.Fatalf("config_path = %q want %q", got.Facts["config_path"], want)
	}
	// An absolute path outside home still names a layout, a mount, or an
	// employer, so the report says it was outside home rather than where.
	if got.Facts["engine_root"] != pathOutsideHome {
		t.Fatalf("engine_root = %q want %q", got.Facts["engine_root"], pathOutsideHome)
	}
}

// Secret-shaped detail keys never reach the stop-screen report, so a probe that grows
// one cannot leak through this surface.
func TestBuildCatastrophicDetailDropsSecretShapedKeys(t *testing.T) {
	res := Result{
		ID:   "provider",
		Code: CodeConfigDirUnwritable,
		Detail: map[string]string{
			"api_key": "sk-live-not-a-real-key",
			"safe":    "ok",
		},
	}
	got := BuildCatastrophicDetail(res, testFacts(), time.Now())
	if _, present := got.Facts["api_key"]; present {
		t.Fatalf("secret-shaped key survived: %#v", got.Facts)
	}
	if got.Facts["safe"] != "ok" {
		t.Fatalf("ordinary fact dropped: %#v", got.Facts)
	}
}
