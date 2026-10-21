package systemproxy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKIOCascadesSystemAndUserPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, low, high, user, http, https string
		mode                               Mode
	}{
		{"system only", "[Proxy Settings]\nProxyType=1\nhttpProxy=system:3128\nhttpsProxy=secure:3129\n", "", "", "http://system:3128", "http://secure:3129", ModeManual},
		{"partial overrides", "[Proxy Settings]\nProxyType=1\nhttpProxy=low:3128\nhttpsProxy=low:3129\n", "[Proxy Settings]\nhttpsProxy=high:3129\n", "[Proxy Settings]\nhttpProxy=user:3128\n", "http://user:3128", "http://high:3129", ModeManual},
		{"explicit disable", "[Proxy Settings]\nProxyType=1\nhttpProxy=low:3128\n", "", "[Proxy Settings]\nProxyType=0\n", "", "", ModeNone},
		{"immutable key", "[Proxy Settings]\nProxyType[$i]=1\nhttpProxy[$i]=system:3128\n", "[Proxy Settings]\nhttpProxy=high:3128\n", "[Proxy Settings]\nProxyType=0\nhttpProxy=user:3128\nhttpsProxy=user:3129\n", "http://system:3128", "http://user:3129", ModeManual},
		{"immutable group", "[Proxy Settings][$i]\nProxyType=1\nhttpProxy=system:3128\n", "[Proxy Settings]\nhttpProxy=high:3128\n", "[Proxy Settings]\nProxyType=0\nhttpsProxy=user:3129\n", "http://system:3128", "", ModeManual},
		{"immutable file", "[$i]\n[Proxy Settings]\nProxyType=1\nhttpProxy=system:3128\n", "[Proxy Settings]\nProxyType=0\n", "[Proxy Settings]\nProxyType=0\n", "http://system:3128", "", ModeManual},
		{"deleted override", "[Proxy Settings]\nProxyType=1\nhttpProxy=system:3128\n", "", "[Proxy Settings]\nhttpProxy[$d]\n", "", "", ModeManual},
		{"unrelated nested group", "[General][Nested]\na=b\n[Proxy Settings]\nProxyType=1\nhttpProxy=system:3128\n", "", "", "http://system:3128", "", ModeManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			low, high, user := t.TempDir(), t.TempDir(), t.TempDir()
			for dir, body := range map[string]string{low: tc.low, high: tc.high, user: tc.user} {
				if body != "" {
					testutil.FailErr(t, "write layer", os.WriteFile(filepath.Join(dir, "kioslaverc"), []byte(body), 0o600))
				}
			}
			lookup := func(key string) (string, bool) {
				switch key {
				case "XDG_CONFIG_HOME":
					return user, true
				case "XDG_CONFIG_DIRS":
					return high + ":" + low, true
				}
				return "", false
			}
			config, ok, err := readKIOConfiguration(lookup)
			testutil.FailErr(t, "read layered proxy", err)
			if !ok || config.Mode != tc.mode || config.Source != SourceKIO {
				t.Fatalf("configuration = %+v, found=%v", config, ok)
			}
			for scheme, want := range map[string]string{"http": tc.http, "https": tc.https} {
				proxy, err := config.ProxyFor(mustTarget(t, scheme+"://external.example/"))
				testutil.FailErr(t, "resolve layered proxy", err)
				got := ""
				if proxy != nil {
					got = proxy.String()
				}
				if got != want {
					t.Fatalf("%s proxy = %q, want %q", scheme, got, want)
				}
			}
		})
	}
}

func TestKIOConfigurationDirectoryOrder(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"", []string{"/etc/xdg/kioslaverc", "/home/alice/.config/kioslaverc"}},
		{"/first:/second:/first:relative::/home/alice/.config", []string{"/second/kioslaverc", "/first/kioslaverc", "/home/alice/.config/kioslaverc"}},
	} {
		lookup := func(key string) (string, bool) {
			if key == "XDG_CONFIG_DIRS" {
				return tc.raw, true
			}
			if key == "HOME" {
				return "/home/alice", true
			}
			return "", false
		}
		if got := kioConfigurationPaths(lookup); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("paths = %v, want %v", got, tc.want)
		}
	}
}

func TestKIOEntryOptionsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		fails     bool
	}{
		{"[Proxy Settings]\nhttpProxy[$e]=$PROXY\n", "http://system:3128", false},
		{"[Proxy Settings]\nhttpProxy=http://system\\s3128\n", "http://system 3128", false},
		{"[Proxy Settings]\nhttpProxy=bad\\q\n", "", true},
		{"[Proxy Settings]\nhttpProxy\n", "", true},
		{"[Proxy Settings\nhttpProxy=system:3128\n", "", true},
	} {
		g := kioEntries{values: map[string]string{}, locked: map[string]bool{}}
		_, err := g.merge(tc.raw, func(string) (string, bool) { return "http://system:3128", true })
		if (err != nil) != tc.fails {
			t.Fatalf("merge %q = %v", tc.raw, err)
		}
		if !tc.fails && g.values["httpProxy"] != tc.want {
			t.Fatalf("value = %q, want %q", g.values["httpProxy"], tc.want)
		}
	}
}

func TestKIOInheritedMalformedPolicyDoesNotBecomeDirect(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write malformed inherited policy", os.WriteFile(filepath.Join(dir, "kioslaverc"), []byte("[Proxy Settings]\nProxyType=invalid\n"), 0o600))
	lookup := func(key string) (string, bool) {
		if key == "XDG_CONFIG_DIRS" {
			return dir, true
		}
		return "", false
	}
	if _, ok, err := readKIOConfiguration(lookup); ok || err == nil {
		t.Fatalf("malformed inherited policy = %v, %v", ok, err)
	}
}

func TestKIOEscapedValuesAndUnsupportedForms(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		fails     bool
	}{
		{`a\nb\tc\rd\\e\sf`, "a\nb\tc\rd\\e f", false},
		{`unfinished\`, "", true},
	} {
		value, err := kioString(tc.raw)
		if (err != nil) != tc.fails || value != tc.want {
			t.Fatalf("decode %q = %q, %v", tc.raw, value, err)
		}
	}
	g := kioEntries{values: map[string]string{}, locked: map[string]bool{}}
	if _, err := g.merge("[Proxy Settings]\nhttpProxy[de]=system:3128\n", func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("localized policy silently became direct")
	}
	if got := kioExpand("$$literal/${PROXY}", func(string) (string, bool) { return "configured", true }); got != "$literal/configured" {
		t.Fatalf("expanded = %q", got)
	}
}
