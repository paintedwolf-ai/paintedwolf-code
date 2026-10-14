package gitexec

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/systemproxy"
)

// proxyConfigurationFn is the host's proxy policy; tests replace it.
var proxyConfigurationFn = httpclient.CurrentProxyConfiguration

// ProxyPlanMode is how a Git network invocation receives the host's proxy policy.
type ProxyPlanMode string

const (
	// ProxyPlanPerHost hands Git the whole policy, so Git, its HTTP helper and
	// Git LFS decide per destination host exactly as host clients would.
	ProxyPlanPerHost ProxyPlanMode = "per-host"
	// ProxyPlanPerRemote applies the remote's own decision to every host the
	// invocation contacts, because the policy cannot be stated in Git's grammar.
	ProxyPlanPerRemote ProxyPlanMode = "per-remote"
)

// ProxyPlanReasonKind names why a policy cannot be handed to Git whole.
type ProxyPlanReasonKind string

const (
	// ProxyReasonException is an exception entry outside the shared grammar.
	ProxyReasonException ProxyPlanReasonKind = "exception_not_expressible"
	// ProxyReasonInverted is an exception list that names the proxied hosts.
	ProxyReasonInverted ProxyPlanReasonKind = "exceptions_inverted"
	// ProxyReasonAutomatic is a proxy chosen by evaluating a script per URL.
	ProxyReasonAutomatic ProxyPlanReasonKind = "automatic_configuration"
	// ProxyReasonMixedSources is an environment proxy beside a system proxy
	// whose exceptions apply only to the system one.
	ProxyReasonMixedSources ProxyPlanReasonKind = "mixed_sources"
	// ProxyReasonSystemUnavailable is a system configuration that could not be read.
	ProxyReasonSystemUnavailable ProxyPlanReasonKind = "system_unavailable"
)

// ProxyPlanReason is one fact that selected ProxyPlanPerRemote.
type ProxyPlanReason struct {
	Kind ProxyPlanReasonKind
	// Detail is the configured entry or the underlying error.
	Detail string
}

// ProxyPlan is the proxy environment one Git network invocation receives.
type ProxyPlan struct {
	Mode    ProxyPlanMode
	Reasons []ProxyPlanReason
	env     []string
}

// Environment returns the variables Git, curl and Git LFS read.
func (p ProxyPlan) Environment() []string { return append([]string(nil), p.env...) }

// Loopback is always direct for host clients; Git needs it stated.
var loopbackExceptions = []string{"localhost", "127.0.0.0/8", "::1"}

// NetworkProxyPlan returns the proxy plan for a network invocation against remote.
func NetworkProxyPlan(remote string) (ProxyPlan, error) {
	return planFor(proxyConfigurationFn(), remote)
}

func planFor(config httpclient.ProxyConfiguration, remote string) (ProxyPlan, error) {
	env, reasons := perHostEnvironment(config)
	if len(reasons) == 0 {
		return ProxyPlan{Mode: ProxyPlanPerHost, env: env}, nil
	}
	plan := ProxyPlan{Mode: ProxyPlanPerRemote, Reasons: reasons}
	target, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || target.Host == "" || !httpScheme(target.Scheme) {
		// A remote outside HTTP gives no decision to extend; Git keeps the
		// launch environment's variables.
		return plan, nil
	}
	proxy, err := config.ProxyFor(target)
	if err != nil {
		return ProxyPlan{}, fmt.Errorf("gitexec: %w", err)
	}
	value := ""
	if proxy != nil {
		value = proxy.String()
	}
	plan.env = curlEnvironment(value, value, "")
	return plan, nil
}

// perHostEnvironment states the policy in the grammar curl and Git LFS share:
// per-scheme proxies and a no_proxy list of `*`, names (covering subdomains),
// address literals and CIDR ranges.
func perHostEnvironment(config httpclient.ProxyConfiguration) ([]string, []ProxyPlanReason) {
	var reasons []ProxyPlanReason
	proxies := map[string]*url.URL{}
	fromSystem, fromEnvironment := false, false
	for _, scheme := range []string{"http", "https"} {
		if explicit := config.ExplicitProxy(scheme); explicit != nil {
			proxies[scheme], fromEnvironment = explicit, true
			continue
		}
		if config.SystemErr != nil {
			reasons = appendReason(reasons, ProxyPlanReason{Kind: ProxyReasonSystemUnavailable, Detail: config.SystemErr.Error()})
			continue
		}
		switch config.System.Mode {
		case systemproxy.ModeAuto:
			reasons = appendReason(reasons, ProxyPlanReason{Kind: ProxyReasonAutomatic, Detail: string(config.System.Source)})
		case systemproxy.ModeManual:
			if config.System.ExceptionsInverted {
				reasons = appendReason(reasons, ProxyPlanReason{Kind: ProxyReasonInverted, Detail: string(config.System.Source)})
				continue
			}
			if proxy := config.System.SchemeProxy(scheme); proxy != nil {
				proxies[scheme], fromSystem = proxy, true
			}
		}
	}
	exceptions := append([]string(nil), loopbackExceptions...)
	listed := config.EnvironmentExceptions
	if fromSystem {
		if fromEnvironment && len(config.System.Exceptions) > 0 {
			reasons = appendReason(reasons, ProxyPlanReason{Kind: ProxyReasonMixedSources, Detail: string(config.System.Source)})
		}
		listed = append(append([]systemproxy.Exception(nil), listed...), config.System.Exceptions...)
	}
	for _, exception := range listed {
		entry, ok := noProxyEntry(exception)
		if !ok {
			reasons = append(reasons, ProxyPlanReason{Kind: ProxyReasonException, Detail: exception.Raw})
			continue
		}
		if entry == "*" {
			exceptions = []string{"*"}
			break
		}
		exceptions = append(exceptions, entry)
	}
	if len(reasons) > 0 {
		return nil, reasons
	}
	return curlEnvironment(proxyValue(proxies["http"]), proxyValue(proxies["https"]), strings.Join(exceptions, ",")), nil
}

// noProxyEntry states an exception in the grammar curl and Go's NO_PROXY share.
func noProxyEntry(exception systemproxy.Exception) (string, bool) {
	if exception.Port != "" {
		return "", false
	}
	switch exception.Kind {
	case systemproxy.ExceptionAll:
		return "*", true
	case systemproxy.ExceptionDomain:
		return exception.Domain, true
	case systemproxy.ExceptionAddress:
		return exception.Address.String(), true
	case systemproxy.ExceptionNetwork:
		return exception.Network.String(), true
	default:
		return "", false
	}
}

// curlEnvironment sets both spellings: curl reads the lowercase names first
// and Git LFS the uppercase ones, and an inherited value must not survive.
func curlEnvironment(httpProxy, httpsProxy, noProxy string) []string {
	return []string{
		"http_proxy=" + httpProxy, "HTTP_PROXY=" + httpProxy,
		"https_proxy=" + httpsProxy, "HTTPS_PROXY=" + httpsProxy,
		"all_proxy=", "ALL_PROXY=",
		"no_proxy=" + noProxy, "NO_PROXY=" + noProxy,
	}
}

func proxyValue(proxy *url.URL) string {
	if proxy == nil {
		return ""
	}
	return proxy.String()
}

func appendReason(reasons []ProxyPlanReason, reason ProxyPlanReason) []ProxyPlanReason {
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func httpScheme(scheme string) bool {
	scheme = strings.ToLower(scheme)
	return scheme == "http" || scheme == "https"
}
