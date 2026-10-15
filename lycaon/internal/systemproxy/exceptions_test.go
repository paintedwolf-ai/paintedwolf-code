package systemproxy

import (
	"testing"
)

func TestDesktopExceptionGrammar(t *testing.T) {
	for entry, tc := range map[string]struct {
		kind  ExceptionKind
		cases map[string]bool
	}{
		"*":               {ExceptionAll, map[string]bool{"anything.example": true, "10.0.0.1": true}},
		"corp.example":    {ExceptionDomain, map[string]bool{"corp.example": true, "a.corp.example": true, "notcorp.example": false}},
		"*.corp.example":  {ExceptionDomain, map[string]bool{"corp.example": true, "a.b.corp.example": true}},
		".Corp.Example":   {ExceptionDomain, map[string]bool{"corp.example": true, "a.corp.example": true}},
		"192.0.2.7":       {ExceptionAddress, map[string]bool{"192.0.2.7": true, "192.0.2.8": false, "corp.example": false}},
		"[2001:db8::1]":   {ExceptionAddress, map[string]bool{"2001:db8::1": true, "[2001:db8::1]": true}},
		"10.0.0.0/8":      {ExceptionNetwork, map[string]bool{"10.2.3.4": true, "11.0.0.1": false, "ten.example": false}},
		"169.254/16":      {ExceptionNetwork, map[string]bool{"169.254.9.9": true, "169.255.0.1": false}},
		"2001:db8::/32":   {ExceptionNetwork, map[string]bool{"2001:db8:1::5": true, "2001:db9::1": false}},
		"build-*.example": {ExceptionGlob, map[string]bool{"build-7.example": true, "build-a.b.example": true, "build.example": false}},
		"*corp.example":   {ExceptionGlob, map[string]bool{"notcorp.example": true, "corp.example": true, "corp.example.org": false}},
		"host?.example":   {ExceptionGlob, map[string]bool{"host1.example": true, "host12.example": false}},
		"10.0.0.0/40":     {ExceptionInvalid, map[string]bool{"10.0.0.1": false}},
		"bad host!":       {ExceptionInvalid, map[string]bool{"bad host!": false}},
		"a..example":      {ExceptionInvalid, map[string]bool{"a..example": false}},
	} {
		exception := ParseDesktopException(entry)
		if exception.Kind != tc.kind || exception.Raw != entry {
			t.Fatalf("%q parsed as %+v, want kind %d", entry, exception, tc.kind)
		}
		for host, want := range tc.cases {
			if got := exception.Matches(host, "443"); got != want {
				t.Fatalf("%q on %s = %t, want %t", entry, host, got, want)
			}
		}
	}
	simple := Exception{Kind: ExceptionSimpleHostnames}
	if !simple.Matches("intranet", "80") || simple.Matches("intranet.example", "80") || simple.Matches("10.0.0.1", "80") {
		t.Fatal("simple host name exception matched the wrong hosts")
	}
	ported := Exception{Kind: ExceptionDomain, Domain: "corp.example", Port: "8443"}
	if !ported.Matches("corp.example", "8443") || ported.Matches("corp.example", "443") {
		t.Fatal("port-restricted exception ignored the port")
	}
	if (Exception{Kind: ExceptionAll}).Matches("", "443") {
		t.Fatal("an empty host matched")
	}
}

func TestMacOSSettingsMapToTheSharedConfiguration(t *testing.T) {
	config := configurationFromMacSettings(macSettings{
		HTTPEnabled: true, HTTPHost: "proxy.example", HTTPPort: 3128,
		HTTPSEnabled: true, HTTPSHost: "secure.example", HTTPSPort: 3129,
		SOCKSEnabled: false, SOCKSHost: "socks.example", SOCKSPort: 1080,
		Exceptions:             []string{"*.local", "169.254/16", "corp.example"},
		ExcludeSimpleHostnames: true,
	})
	if config.Source != SourceMacOS || config.Mode != ModeManual || config.HTTP.String() != "http://proxy.example:3128" ||
		config.HTTPS.String() != "http://secure.example:3129" || config.SOCKS != nil {
		t.Fatalf("manual configuration = %+v", config)
	}
	kinds := []ExceptionKind{}
	for _, exception := range config.Exceptions {
		kinds = append(kinds, exception.Kind)
	}
	if len(kinds) != 4 || kinds[0] != ExceptionDomain || kinds[1] != ExceptionNetwork || kinds[3] != ExceptionSimpleHostnames {
		t.Fatalf("exceptions = %+v", config.Exceptions)
	}
	for target, want := range map[string]string{
		"https://api.example/":    "http://secure.example:3129",
		"http://api.example/":     "http://proxy.example:3128",
		"https://printer.local/":  "",
		"https://169.254.1.1/":    "",
		"https://intranet/":       "",
		"https://a.corp.example/": "",
	} {
		proxy, err := config.ProxyFor(mustTarget(t, target))
		got := ""
		if proxy != nil {
			got = proxy.String()
		}
		if err != nil || got != want {
			t.Fatalf("%s = %q, %v; want %q", target, got, err, want)
		}
	}

	if config := configurationFromMacSettings(macSettings{HTTPEnabled: true, HTTPHost: "proxy.example", HTTPPort: 3128, AutoConfigEnabled: true, AutoConfigURL: "http://wpad.example/proxy.pac"}); config.Mode != ModeAuto || config.Script != "http://wpad.example/proxy.pac" {
		t.Fatalf("auto-configuration = %+v", config)
	}
	if config := configurationFromMacSettings(macSettings{AutoDiscoveryEnabled: true, AutoConfigURL: "ignored"}); config.Mode != ModeAuto || config.Script != "" {
		t.Fatalf("auto-discovery = %+v", config)
	}
	if config := configurationFromMacSettings(macSettings{HTTPEnabled: true, Exceptions: []string{"corp.example"}}); config.Mode != ModeNone || len(config.Exceptions) != 0 {
		t.Fatalf("enabled proxy without a host = %+v", config)
	}
}

func TestInvertedExceptionsProxyOnlyListedHosts(t *testing.T) {
	config := Configuration{Source: SourceKIO, Mode: ModeManual, HTTPS: mustTarget(t, "http://proxy.example:3128"),
		Exceptions: []Exception{ParseDesktopException("corp.example")}, ExceptionsInverted: true}
	if proxy, err := config.ProxyFor(mustTarget(t, "https://a.corp.example/")); err != nil || proxy == nil {
		t.Fatalf("listed host = %v, %v", proxy, err)
	}
	if proxy, err := config.ProxyFor(mustTarget(t, "https://other.example/")); err != nil || proxy != nil {
		t.Fatalf("unlisted host = %v, %v", proxy, err)
	}
}
