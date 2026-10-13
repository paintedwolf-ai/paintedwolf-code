package systemproxy

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const gnomeManualDump = `org.gnome.system.proxy autoconfig-url ''
org.gnome.system.proxy ignore-hosts ['localhost', '127.0.0.0/8', '::1', '*.corp.example', '.internal.example', '10.1.2.3']
org.gnome.system.proxy mode 'manual'
org.gnome.system.proxy use-same-proxy true
org.gnome.system.proxy.ftp host ''
org.gnome.system.proxy.ftp port 0
org.gnome.system.proxy.http authentication-password 'p\'w\\d'
org.gnome.system.proxy.http authentication-user 'alice'
org.gnome.system.proxy.http enabled true
org.gnome.system.proxy.http host 'proxy.example'
org.gnome.system.proxy.http port 3128
org.gnome.system.proxy.http use-authentication true
org.gnome.system.proxy.https host ''
org.gnome.system.proxy.https port 0
org.gnome.system.proxy.socks host 'socks.example'
org.gnome.system.proxy.socks port 1080
`

func mustTarget(t *testing.T, raw string) *url.URL {
	t.Helper()
	target, err := url.Parse(raw)
	testutil.FailErr(t, "parse target", err)
	return target
}

func TestGNOMEManualProxyAppliesAuthenticationSharingAndExceptions(t *testing.T) {
	config, err := parseGSettingsDump([]byte(gnomeManualDump))
	testutil.FailErr(t, "parse GNOME dump", err)
	if config.Source != SourceGSettings || config.Mode != modeManual || !config.SameProxy {
		t.Fatalf("configuration = %+v", config)
	}
	for target, want := range map[string]string{
		"https://api.example/v1":      "http://alice:p%27w%5Cd@proxy.example:3128",
		"http://plain.example/":       "http://alice:p%27w%5Cd@proxy.example:3128",
		"https://host.corp.example/":  "",
		"https://corp.example/":       "",
		"https://x.internal.example/": "",
		"https://10.1.2.3/":           "",
		"https://127.0.0.9/":          "",
		"https://[::1]:8787/":         "",
		"https://notcorp.example/":    "http://alice:p%27w%5Cd@proxy.example:3128",
	} {
		proxy, err := config.proxyFor(mustTarget(t, target))
		testutil.FailErr(t, "resolve "+target, err)
		got := ""
		if proxy != nil {
			got = proxy.String()
		}
		if got != want {
			t.Fatalf("%s proxy = %q, want %q", target, got, want)
		}
	}
	proxy, err := config.proxyFor(mustTarget(t, "https://api.example/"))
	testutil.FailErr(t, "resolve authenticated proxy", err)
	if redacted := Redact(proxy); strings.Contains(redacted, "alice") || strings.Contains(redacted, "p%27") || redacted != "http://redacted@proxy.example:3128" {
		t.Fatalf("redacted proxy = %q", redacted)
	}
	if Redact(nil) != "direct" {
		t.Fatal("direct connection was not described")
	}
}

func TestGNOMESeparateProxiesFallBackToSOCKS(t *testing.T) {
	dump := strings.NewReplacer(
		"use-same-proxy true", "use-same-proxy false",
		"use-authentication true", "use-authentication false",
	).Replace(gnomeManualDump)
	config, err := parseGSettingsDump([]byte(dump))
	testutil.FailErr(t, "parse GNOME dump", err)
	proxy, err := config.proxyFor(mustTarget(t, "https://api.example/"))
	testutil.FailErr(t, "resolve https", err)
	if proxy == nil || proxy.String() != "socks5://socks.example:1080" {
		t.Fatalf("https proxy = %v", proxy)
	}
	proxy, err = config.proxyFor(mustTarget(t, "http://api.example/"))
	testutil.FailErr(t, "resolve http", err)
	if proxy == nil || proxy.String() != "http://proxy.example:3128" {
		t.Fatalf("http proxy = %v, want unauthenticated http proxy", proxy)
	}
}

func TestGNOMEModesNoneAndAuto(t *testing.T) {
	config, err := parseGSettingsDump([]byte(strings.Replace(gnomeManualDump, "mode 'manual'", "mode 'none'", 1)))
	testutil.FailErr(t, "parse none", err)
	if proxy, err := config.proxyFor(mustTarget(t, "https://api.example/")); proxy != nil || err != nil {
		t.Fatalf("none mode = %v, %v", proxy, err)
	}
	auto := strings.NewReplacer("mode 'manual'", "mode 'auto'", "autoconfig-url ''", "autoconfig-url 'http://wpad.example/proxy.pac'").Replace(gnomeManualDump)
	config, err = parseGSettingsDump([]byte(auto))
	testutil.FailErr(t, "parse auto", err)
	proxy, err := config.proxyFor(mustTarget(t, "https://api.example/"))
	var unsupported *Unsupported
	if proxy != nil || !errors.As(err, &unsupported) {
		t.Fatalf("auto mode = %v, %v", proxy, err)
	}
	if unsupported.Source != SourceGSettings || unsupported.Mechanism != "proxy auto-configuration" || unsupported.Reference != "http://wpad.example/proxy.pac" {
		t.Fatalf("unsupported = %+v", unsupported)
	}
	if message := unsupported.Error(); !strings.Contains(message, "HTTPS_PROXY") || !strings.Contains(message, "proxy.pac") {
		t.Fatalf("unsupported message = %q", message)
	}
	config, err = parseGSettingsDump([]byte(strings.Replace(gnomeManualDump, "mode 'manual'", "mode 'auto'", 1)))
	testutil.FailErr(t, "parse discovery", err)
	if _, err := config.proxyFor(mustTarget(t, "https://api.example/")); !errors.As(err, &unsupported) || unsupported.Mechanism != "proxy auto-discovery" {
		t.Fatalf("discovery = %v", err)
	}
}

func TestGNOMEDumpRefusesMalformedNotation(t *testing.T) {
	for name, dump := range map[string]string{
		"unknown mode":    strings.Replace(gnomeManualDump, "mode 'manual'", "mode 'weird'", 1),
		"foreign schema":  "org.other.schema key 'x'\n",
		"bad string":      "org.gnome.system.proxy mode 'manual\n",
		"bad array":       "org.gnome.system.proxy ignore-hosts ['a' 'b']\n",
		"bad integer":     "org.gnome.system.proxy.http port 80x\n",
		"missing fields":  "org.gnome.system.proxy mode\n",
		"too many lines":  strings.Repeat("org.gnome.system.proxy mode 'none'\n", maxGSettingsLines+1),
		"oversized":       strings.Repeat("x", maxGSettingsBytes+1),
		"numeric type":    "org.gnome.system.proxy.http port 1 2\n",
		"non-string list": "org.gnome.system.proxy ignore-hosts [1, 2]\n",
	} {
		if _, err := parseGSettingsDump([]byte(dump)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestGVariantNotationCoversSchemaTypes(t *testing.T) {
	for text, want := range map[string]gsettingsValue{
		"true":                       {kind: 'b', boolean: true},
		"false":                      {kind: 'b'},
		"8080":                       {kind: 'i', number: 8080},
		"uint16 8080":                {kind: 'i', number: 8080},
		"'a\\tb'":                    {kind: 's', text: "a\tb"},
		"\"quoted\"":                 {kind: 's', text: "quoted"},
		"@as []":                     {kind: 'a', list: []string{}},
		"[]":                         {kind: 'a', list: []string{}},
		"['one', \"two\", 'th,ree']": {kind: 'a', list: []string{"one", "two", "th,ree"}},
	} {
		got, err := parseGVariant(text)
		testutil.FailErr(t, "parse "+text, err)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %+v, want %+v", text, got, want)
		}
	}
	for _, text := range []string{"'a\\qb'", "'open", "[", "['a',]", "nope", " 12 34", "'a' 'b'"} {
		if _, err := parseGVariant(text); err == nil {
			t.Fatalf("%q was accepted", text)
		}
	}
}

func writeKIO(t *testing.T, body string) func(string) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "write kioslaverc", os.WriteFile(filepath.Join(dir, "kioslaverc"), []byte(body), 0o600))
	return func(key string) (string, bool) {
		switch key {
		case "XDG_CONFIG_HOME":
			return dir, true
		case "CORP_HTTPS_PROXY":
			return "http://env.example:9000", true
		}
		return "", false
	}
}

func TestKDEManualProxyParsesSpacedPortsAndExceptions(t *testing.T) {
	lookup := writeKIO(t, "[General]\nx=1\n\n[Proxy Settings]\nProxyType=1\nhttpProxy=http://proxy.example 3128\nhttpsProxy=secure.example:3129\nsocksProxy=socks://socks.example:1080\nNoProxyFor=localhost,.corp.example, 10.0.0.0/8\nReversedException=false\nAuthMode[$i]=0\n")
	config, ok, err := readKIOConfiguration(lookup)
	testutil.FailErr(t, "read kioslaverc", err)
	if !ok || config.Source != SourceKIO || config.Mode != modeManual {
		t.Fatalf("configuration = %+v, %v", config, ok)
	}
	for target, want := range map[string]string{
		"https://api.example/":    "http://secure.example:3129",
		"http://api.example/":     "http://proxy.example:3128",
		"https://a.corp.example/": "",
		"https://10.2.3.4/":       "",
		"ftp://files.example/":    "socks5://socks.example:1080",
	} {
		proxy, err := config.proxyFor(mustTarget(t, target))
		testutil.FailErr(t, "resolve "+target, err)
		got := ""
		if proxy != nil {
			got = proxy.String()
		}
		if got != want {
			t.Fatalf("%s proxy = %q, want %q", target, got, want)
		}
	}
}

func TestKDEReversedExceptionProxiesOnlyListedHosts(t *testing.T) {
	lookup := writeKIO(t, "[Proxy Settings]\nProxyType=1\nhttpsProxy=proxy.example:3128\nNoProxyFor=api.example\nReversedException=true\n")
	config, _, err := readKIOConfiguration(lookup)
	testutil.FailErr(t, "read kioslaverc", err)
	if proxy, err := config.proxyFor(mustTarget(t, "https://api.example/")); err != nil || proxy == nil {
		t.Fatalf("listed host proxy = %v, %v", proxy, err)
	}
	if proxy, err := config.proxyFor(mustTarget(t, "https://other.example/")); err != nil || proxy != nil {
		t.Fatalf("unlisted host proxy = %v, %v", proxy, err)
	}
}

func TestKDEEnvironmentTypeResolvesNamedVariables(t *testing.T) {
	lookup := writeKIO(t, "[Proxy Settings]\nProxyType=3\nhttpsProxy=CORP_HTTPS_PROXY\nhttpProxy=UNSET_VARIABLE\n")
	config, _, err := readKIOConfiguration(lookup)
	testutil.FailErr(t, "read kioslaverc", err)
	if config.HTTPS == nil || config.HTTPS.String() != "http://env.example:9000" || config.HTTP != nil {
		t.Fatalf("configuration = %+v", config)
	}
}

func TestKDEScriptAndDiscoveryAreUnsupported(t *testing.T) {
	config, _, err := readKIOConfiguration(writeKIO(t, "[Proxy Settings]\nProxyType=2\nProxy Config Script=file:///etc/proxy.pac\n"))
	testutil.FailErr(t, "read script type", err)
	var unsupported *Unsupported
	if _, err := config.proxyFor(mustTarget(t, "https://api.example/")); !errors.As(err, &unsupported) || unsupported.Source != SourceKIO || unsupported.Reference != "file:///etc/proxy.pac" {
		t.Fatalf("script type = %v", err)
	}
	config, _, err = readKIOConfiguration(writeKIO(t, "[Proxy Settings]\nProxyType=4\n"))
	testutil.FailErr(t, "read discovery type", err)
	if _, err := config.proxyFor(mustTarget(t, "https://api.example/")); !errors.As(err, &unsupported) || unsupported.Mechanism != "proxy auto-discovery" {
		t.Fatalf("discovery type = %v", err)
	}
}

func TestKDEAbsenceAndMalformedFiles(t *testing.T) {
	if _, ok, err := readKIOConfiguration(func(string) (string, bool) { return "", false }); ok || err != nil {
		t.Fatalf("no home = %v, %v", ok, err)
	}
	if _, ok, err := readKIOConfiguration(writeKIO(t, "[General]\nx=1\n")); ok || err != nil {
		t.Fatalf("file without proxy group = %v, %v", ok, err)
	}
	missing := t.TempDir()
	if _, ok, err := readKIOConfiguration(func(string) (string, bool) { return missing, true }); ok || err != nil {
		t.Fatalf("missing file = %v, %v", ok, err)
	}
	if _, ok, err := readKIOConfiguration(writeKIO(t, "[Proxy Settings]\nProxyType=0\n")); !ok || err != nil {
		t.Fatalf("none type = %v, %v", ok, err)
	}
	for name, body := range map[string]string{
		"bad type":     "[Proxy Settings]\nProxyType=abc\n",
		"unknown type": "[Proxy Settings]\nProxyType=9\n",
		"bad port":     "[Proxy Settings]\nProxyType=1\nhttpProxy=proxy.example x\n",
		"no port":      "[Proxy Settings]\nProxyType=1\nhttpProxy=proxy.example\n",
		"bad scheme":   "[Proxy Settings]\nProxyType=1\nhttpProxy=ftp://proxy.example:21\n",
		"oversized":    "[Proxy Settings]\nProxyType=1\n#" + strings.Repeat("x", maxKIOBytes) + "\n",
	} {
		if _, _, err := readKIOConfiguration(writeKIO(t, body)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestKIOConfigurationPathFollowsXDGThenHome(t *testing.T) {
	home := func(key string) (string, bool) {
		if key == "HOME" {
			return "/home/alice", true
		}
		return "", false
	}
	if got := kioConfigurationPath(home); got != "/home/alice/.config/kioslaverc" {
		t.Fatalf("home path = %q", got)
	}
	if got := kioConfigurationPath(func(string) (string, bool) { return "relative", true }); got != "" {
		t.Fatalf("relative directories were accepted: %q", got)
	}
}
