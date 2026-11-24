package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/wiring"
)

const ghFixtureVerb = "pw-gh-fixture"

func TestMain(m *testing.M) {
	if len(os.Args) >= 4 && os.Args[1] == ghFixtureVerb && os.Args[2] == "auth" && os.Args[3] == "status" {
		os.Exit(runFixtureGHAuthStatus())
	}
	if filepath.Base(os.Args[0]) == "gh" && len(os.Args) >= 3 && os.Args[1] == "auth" && os.Args[2] == "status" {
		os.Exit(runFixtureGHAuthStatus())
	}
	testutil.SelectRaceTests(
		"TestWorkflowBackgroundBuildWaitAndStatusCheck",
	)
	gittestsetup.Enable()
	os.Exit(wiring.Main(m))
}

func runFixtureGHAuthStatus() int {
	var token string
	switch runtime.GOOS {
	case "darwin":
		raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "Library", "Keychains", "login.keychain-db"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "You are not logged into any GitHub hosts. To log in, run: gh auth login")
			return 1
		}
		token = strings.TrimSpace(string(raw))
	case "linux":
		const (
			credRoot dbus.ObjectPath = "/org/freedesktop/secrets"
			credItem dbus.ObjectPath = "/org/freedesktop/secrets/collection/test/item"
		)
		type credSecret struct {
			Session     dbus.ObjectPath
			Parameters  []byte
			Value       []byte
			ContentType string
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		addr := strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS"))
		if addr == "" {
			if rt := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); rt != "" {
				addr = "unix:path=" + filepath.Join(rt, "bus")
			} else {
				addr = fmt.Sprintf("unix:path=/run/user/%d/bus", os.Getuid())
			}
		}
		bus, err := dbus.Connect(addr, dbus.WithContext(ctx))
		if err != nil {
			fmt.Fprintln(os.Stderr, "You are not logged into any GitHub hosts. To log in, run: gh auth login")
			return 1
		}
		defer func() { _ = bus.Close() }()
		var outVariant dbus.Variant
		var sessionPath dbus.ObjectPath
		if err := bus.Object("org.freedesktop.secrets", credRoot).CallWithContext(ctx, "org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&outVariant, &sessionPath); err != nil {
			fmt.Fprintln(os.Stderr, "You are not logged into any GitHub hosts. To log in, run: gh auth login")
			return 1
		}
		var secret credSecret
		if err := bus.Object("org.freedesktop.secrets", credItem).CallWithContext(ctx, "org.freedesktop.Secret.Item.GetSecret", 0, sessionPath).Store(&secret); err != nil {
			fmt.Fprintln(os.Stderr, "You are not logged into any GitHub hosts. To log in, run: gh auth login")
			return 1
		}
		_ = bus.Object("org.freedesktop.secrets", sessionPath).CallWithContext(ctx, "org.freedesktop.Secret.Session.Close", 0).Err
		token = strings.TrimSpace(string(secret.Value))
	default:
		fmt.Fprintln(os.Stderr, "unsupported fixture platform")
		return 1
	}

	apiURL := strings.TrimSpace(os.Getenv("GH_FIXTURE_API_URL"))
	if apiURL == "" {
		if cfgRaw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config", "gh", "hosts.yml")); err == nil {
			for _, line := range strings.Split(string(cfgRaw), "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "api_url:"); ok {
					apiURL = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	if apiURL == "" {
		fmt.Fprintln(os.Stdout, "Logged in to github.com")
		return 0
	}
	transport := &http.Transport{}
	for _, k := range []string{"HTTP_PROXY", "http_proxy"} {
		if rawProxy := strings.TrimSpace(os.Getenv(k)); rawProxy != "" {
			if parsed, err := url.Parse(rawProxy); err == nil {
				transport.Proxy = http.ProxyURL(parsed)
				break
			}
		}
	}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error connecting to api.github.com:", err)
		return 1
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintln(os.Stderr, "github.com")
		fmt.Fprintln(os.Stderr, "  X Authentication failed (HTTP 401: Bad credentials)")
		return 1
	}
	fmt.Fprintln(os.Stdout, "github.com")
	fmt.Fprintln(os.Stdout, "  ✓ Logged in to github.com")
	return 0
}
