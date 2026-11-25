package hostresources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type discoveryEnvironment struct {
	homeDir            string
	configDir          string
	runtimeDir         string
	platform           string
	lookPath           func(string) (string, error)
	lookupEnv          func(string) (string, bool)
	stat               func(string) (os.FileInfo, error)
	httpClient         func(time.Duration) *http.Client
	namedPipeAvailable func(string) (bool, error)
}

func defaultDiscoveryEnvironment(configDir string) discoveryEnvironment {
	home, _ := os.UserHomeDir()
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	return discoveryEnvironment{
		homeDir: home, configDir: configDir, runtimeDir: runtimeDir,
		platform: currentPlatform(),
		lookPath: resolvedLookPath, lookupEnv: os.LookupEnv, stat: os.Stat,
		namedPipeAvailable: platformNamedPipeAvailable,
		httpClient: func(timeout time.Duration) *http.Client {
			return &http.Client{
				Timeout: timeout,
				Transport: &http.Transport{
					Proxy:             nil,
					DisableKeepAlives: true,
					DialContext:       loopbackDialContext(timeout),
				},
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
		},
	}
}

func currentPlatform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

func loopbackDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := net.Dialer{Timeout: timeout}
		for _, candidate := range addresses {
			if !candidate.IP.IsLoopback() {
				continue
			}
			connection, dialErr := dialer.DialContext(
				ctx, network, net.JoinHostPort(candidate.IP.String(), port),
			)
			if dialErr == nil {
				return connection, nil
			}
			err = dialErr
		}
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("loopback probe resolved no loopback address")
	}
}

type verdict struct {
	status   Status
	reason   string
	captures map[string]string
}

func discover(ctx context.Context, def Definition, env discoveryEnvironment) (verdict, *Realization) {
	realization := realizationForPlatform(def.Realizations, env.platform)
	if realization == nil {
		return verdict{status: StatusUnavailable, reason: "platform_not_applicable"}, nil
	}
	return evaluate(ctx, realization.Discover, env), realization
}

func realizationForPlatform(realizations []Realization, platform string) *Realization {
	for i := range realizations {
		if len(realizations[i].Platforms) == 0 || stringSetIncludes(realizations[i].Platforms, platform) {
			return &realizations[i]
		}
	}
	return nil
}

func stringSetIncludes(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func evaluate(ctx context.Context, expr Expression, env discoveryEnvironment) verdict {
	switch {
	case len(expr.All) > 0:
		return evaluateAll(ctx, expr.All, env)
	case len(expr.Any) > 0:
		return evaluateAny(ctx, expr.Any, env)
	case expr.Executable != nil:
		return evaluateExecutable(*expr.Executable, env)
	case expr.Path != nil:
		return evaluatePath(*expr.Path, env)
	case expr.Environment != nil:
		return evaluateEnvironment(*expr.Environment, env)
	case expr.LoopbackHTTP != nil:
		return evaluateLoopbackHTTP(ctx, *expr.LoopbackHTTP, env)
	case expr.NamedPipe != nil:
		return evaluateNamedPipe(*expr.NamedPipe, env)
	default:
		return verdict{status: StatusUnknown, reason: "invalid_probe"}
	}
}

func evaluateNamedPipe(probe NamedPipeProbe, env discoveryEnvironment) verdict {
	if env.platform != "windows" || env.namedPipeAvailable == nil {
		return verdict{status: StatusUnavailable, reason: "platform_not_applicable"}
	}
	for _, name := range probe.Names {
		available, err := env.namedPipeAvailable(name)
		if err != nil {
			return verdict{status: StatusUnknown, reason: "named_pipe_probe_failed"}
		}
		if available {
			return verdict{status: StatusAvailable, captures: capture(probe.Capture, name)}
		}
	}
	return verdict{status: StatusUnavailable, reason: "named_pipe_not_found"}
}

func evaluateAll(ctx context.Context, expressions []Expression, env discoveryEnvironment) verdict {
	merged := map[string]string{}
	unknownReason := ""
	for _, expr := range expressions {
		got := evaluate(ctx, expr, env)
		if got.status == StatusUnavailable {
			return got
		}
		if got.status == StatusUnknown && unknownReason == "" {
			unknownReason = got.reason
		}
		mergeCaptures(merged, got.captures)
	}
	if unknownReason != "" {
		return verdict{status: StatusUnknown, reason: unknownReason, captures: merged}
	}
	return verdict{status: StatusAvailable, captures: merged}
}

func evaluateAny(ctx context.Context, expressions []Expression, env discoveryEnvironment) verdict {
	unknownReason := ""
	unavailableReason := "not_detected"
	for _, expr := range expressions {
		got := evaluate(ctx, expr, env)
		if got.status == StatusAvailable {
			return got
		}
		if got.status == StatusUnknown && unknownReason == "" {
			unknownReason = got.reason
		}
		if got.status == StatusUnavailable && got.reason != "" {
			unavailableReason = got.reason
		}
	}
	if unknownReason != "" {
		return verdict{status: StatusUnknown, reason: unknownReason}
	}
	return verdict{status: StatusUnavailable, reason: unavailableReason}
}

func evaluateExecutable(probe ExecutableProbe, env discoveryEnvironment) verdict {
	for _, name := range probe.Names {
		path, err := env.lookPath(name)
		if err == nil {
			return verdict{status: StatusAvailable, captures: capture(probe.Capture, path)}
		}
		if !errors.Is(err, osexec.ErrNotFound) {
			return verdict{status: StatusUnknown, reason: "executable_probe_failed"}
		}
	}
	return verdict{status: StatusUnavailable, reason: "executable_not_found"}
}

func evaluatePath(probe PathProbe, env discoveryEnvironment) verdict {
	for _, template := range probe.Paths {
		path, ok := expandPath(template, env)
		if !ok {
			continue
		}
		info, err := env.stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return verdict{status: StatusUnknown, reason: "path_probe_failed"}
		}
		if pathKindMatches(info, probe.Kind) {
			return verdict{status: StatusAvailable, captures: capture(probe.Capture, path)}
		}
	}
	return verdict{status: StatusUnavailable, reason: "path_not_found"}
}

func evaluateEnvironment(probe EnvironmentProbe, env discoveryEnvironment) verdict {
	for _, name := range probe.Names {
		if _, present := env.lookupEnv(name); present {
			return verdict{status: StatusAvailable}
		}
	}
	return verdict{status: StatusUnavailable, reason: "environment_unset"}
}

func evaluateLoopbackHTTP(ctx context.Context, probe LoopbackHTTPProbe, env discoveryEnvironment) verdict {
	want := probe.Status
	if want == 0 {
		want = http.StatusOK
	}
	timeout := time.Duration(probe.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	for _, raw := range probe.URLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return verdict{status: StatusUnknown, reason: "loopback_request_invalid"}
		}
		response, err := env.httpClient(timeout).Do(req)
		if err != nil {
			continue
		}
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		_ = response.Body.Close()
		if response.StatusCode == want {
			return verdict{status: StatusAvailable, captures: capture(probe.Capture, raw)}
		}
	}
	return verdict{status: StatusUnavailable, reason: "loopback_unreachable"}
}

func expandPath(template string, env discoveryEnvironment) (string, bool) {
	path := strings.TrimSpace(template)
	replacements := map[string]string{
		"{home}":        env.homeDir,
		"{config_dir}":  env.configDir,
		"{runtime_dir}": env.runtimeDir,
	}
	for token, value := range replacements {
		if strings.Contains(path, token) {
			if value == "" {
				return "", false
			}
			path = strings.ReplaceAll(path, token, value)
		}
	}
	if strings.Contains(path, "{") || !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

func pathKindMatches(info os.FileInfo, kind string) bool {
	switch kind {
	case "file":
		return info.Mode().IsRegular()
	case "directory":
		return info.IsDir()
	case "socket":
		return info.Mode()&os.ModeSocket != 0
	case "any":
		return true
	default:
		return false
	}
}

func capture(name, value string) map[string]string {
	if name == "" {
		return nil
	}
	return map[string]string{name: value}
}

func mergeCaptures(into, from map[string]string) {
	for key, value := range from {
		into[key] = value
	}
}
