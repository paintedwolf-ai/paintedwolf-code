//go:build darwin && cgo

package systemproxy

/*
#cgo LDFLAGS: -framework CoreFoundation -framework CFNetwork
#include <CoreFoundation/CoreFoundation.h>
#include <CFNetwork/CFNetwork.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	CFRunLoopRef loop;
	CFArrayRef proxies;
	CFErrorRef error;
	Boolean done;
} pw_pac_state;

static void pw_pac_callback(void *client, CFArrayRef proxies, CFErrorRef error) {
	pw_pac_state *state = (pw_pac_state *)client;
	if (proxies != NULL) state->proxies = CFRetain(proxies);
	if (error != NULL) state->error = (CFErrorRef)CFRetain(error);
	state->done = true;
	CFRunLoopStop(state->loop);
}

static char *pw_copy_cfstring(CFStringRef value) {
	if (value == NULL) return NULL;
	CFIndex length = CFStringGetLength(value);
	CFIndex maximum = CFStringGetMaximumSizeForEncoding(length, kCFStringEncodingUTF8) + 1;
	char *out = (char *)calloc((size_t)maximum, 1);
	if (out == NULL) return NULL;
	if (!CFStringGetCString(value, out, maximum, kCFStringEncodingUTF8)) {
		free(out);
		return NULL;
	}
	return out;
}

static void pw_set_error(CFErrorRef error, char **message) {
	if (message == NULL || error == NULL) return;
	CFStringRef description = CFErrorCopyDescription(error);
	if (description != NULL) {
		*message = pw_copy_cfstring(description);
		CFRelease(description);
	}
}

static CFArrayRef pw_execute_pac_url(CFURLRef pacURL, CFURLRef targetURL, double timeout, char **message) {
	pw_pac_state state = {0};
	state.loop = CFRunLoopGetCurrent();
	CFStreamClientContext context = {0, &state, NULL, NULL, NULL};
	CFRunLoopSourceRef source = CFNetworkExecuteProxyAutoConfigurationURL(
		pacURL, targetURL, pw_pac_callback, &context);
	if (source == NULL) return NULL;
	CFRunLoopAddSource(state.loop, source, kCFRunLoopDefaultMode);
	CFAbsoluteTime deadline = CFAbsoluteTimeGetCurrent() + timeout;
	while (!state.done && CFAbsoluteTimeGetCurrent() < deadline) {
		double remaining = deadline - CFAbsoluteTimeGetCurrent();
		if (remaining > 0.25) remaining = 0.25;
		CFRunLoopRunInMode(kCFRunLoopDefaultMode, remaining, true);
	}
	CFRunLoopRemoveSource(state.loop, source, kCFRunLoopDefaultMode);
	CFRunLoopSourceInvalidate(source);
	CFRelease(source);
	if (!state.done) {
		*message = strdup("proxy auto-configuration timed out");
		return NULL;
	}
	if (state.error != NULL) {
		pw_set_error(state.error, message);
		CFRelease(state.error);
	}
	return state.proxies;
}

static int pw_copy_proxy_fields(CFArrayRef proxies, CFURLRef targetURL, double timeout,
	char **scheme, char **host, int *port, char **username, char **password, char **message) {
	if (proxies == NULL) return 0;
	CFIndex count = CFArrayGetCount(proxies);
	for (CFIndex i = 0; i < count; i++) {
		CFDictionaryRef proxy = (CFDictionaryRef)CFArrayGetValueAtIndex(proxies, i);
		if (proxy == NULL || CFGetTypeID(proxy) != CFDictionaryGetTypeID()) continue;
		CFStringRef type = (CFStringRef)CFDictionaryGetValue(proxy, kCFProxyTypeKey);
		if (type == NULL) continue;
		if (CFEqual(type, kCFProxyTypeNone)) return 0;
		if (CFEqual(type, kCFProxyTypeAutoConfigurationURL)) {
			CFURLRef pacURL = (CFURLRef)CFDictionaryGetValue(proxy, kCFProxyAutoConfigurationURLKey);
			if (pacURL == NULL) continue;
			CFArrayRef resolved = pw_execute_pac_url(pacURL, targetURL, timeout, message);
			if (resolved == NULL) return -1;
			int result = pw_copy_proxy_fields(resolved, targetURL, timeout,
				scheme, host, port, username, password, message);
			CFRelease(resolved);
			return result;
		}
		if (CFEqual(type, kCFProxyTypeAutoConfigurationJavaScript)) {
			CFStringRef script = (CFStringRef)CFDictionaryGetValue(proxy, kCFProxyAutoConfigurationJavaScriptKey);
			if (script == NULL) continue;
			CFErrorRef error = NULL;
			CFArrayRef resolved = CFNetworkCopyProxiesForAutoConfigurationScript(script, targetURL, &error);
			if (resolved == NULL) {
				pw_set_error(error, message);
				if (error != NULL) CFRelease(error);
				return -1;
			}
			int result = pw_copy_proxy_fields(resolved, targetURL, timeout,
				scheme, host, port, username, password, message);
			CFRelease(resolved);
			return result;
		}

		const char *proxyScheme = NULL;
		if (CFEqual(type, kCFProxyTypeHTTP) || CFEqual(type, kCFProxyTypeHTTPS)) proxyScheme = "http";
		if (CFEqual(type, kCFProxyTypeSOCKS)) proxyScheme = "socks5";
		if (proxyScheme == NULL) continue;
		CFStringRef proxyHost = (CFStringRef)CFDictionaryGetValue(proxy, kCFProxyHostNameKey);
		CFNumberRef proxyPort = (CFNumberRef)CFDictionaryGetValue(proxy, kCFProxyPortNumberKey);
		if (proxyHost == NULL || proxyPort == NULL) continue;
		int value = 0;
		if (!CFNumberGetValue(proxyPort, kCFNumberIntType, &value) || value <= 0 || value > 65535) continue;
		*scheme = strdup(proxyScheme);
		*host = pw_copy_cfstring(proxyHost);
		*port = value;
		*username = pw_copy_cfstring((CFStringRef)CFDictionaryGetValue(proxy, kCFProxyUsernameKey));
		*password = pw_copy_cfstring((CFStringRef)CFDictionaryGetValue(proxy, kCFProxyPasswordKey));
		if (*scheme == NULL || *host == NULL) return -1;
		return 1;
	}
	return 0;
}

static CFURLRef pw_url(const char *raw) {
	if (raw == NULL) return NULL;
	return CFURLCreateWithBytes(kCFAllocatorDefault, (const UInt8 *)raw,
		(CFIndex)strlen(raw), kCFStringEncodingUTF8, NULL);
}

static int pw_system_proxy(const char *target, double timeout,
	char **scheme, char **host, int *port, char **username, char **password, char **message) {
	CFURLRef targetURL = pw_url(target);
	if (targetURL == NULL) return -1;
	CFDictionaryRef settings = CFNetworkCopySystemProxySettings();
	if (settings == NULL) {
		CFRelease(targetURL);
		return 0;
	}
	CFArrayRef proxies = CFNetworkCopyProxiesForURL(targetURL, settings);
	CFRelease(settings);
	int result = pw_copy_proxy_fields(proxies, targetURL, timeout,
		scheme, host, port, username, password, message);
	if (proxies != NULL) CFRelease(proxies);
	CFRelease(targetURL);
	return result;
}

static int pw_pac_script_proxy(const char *script, const char *target,
	char **scheme, char **host, int *port, char **username, char **password, char **message) {
	CFURLRef targetURL = pw_url(target);
	CFStringRef source = CFStringCreateWithCString(kCFAllocatorDefault, script, kCFStringEncodingUTF8);
	if (targetURL == NULL || source == NULL) {
		if (targetURL != NULL) CFRelease(targetURL);
		if (source != NULL) CFRelease(source);
		return -1;
	}
	CFErrorRef error = NULL;
	CFArrayRef proxies = CFNetworkCopyProxiesForAutoConfigurationScript(source, targetURL, &error);
	CFRelease(source);
	if (proxies == NULL) {
		pw_set_error(error, message);
		if (error != NULL) CFRelease(error);
		CFRelease(targetURL);
		return -1;
	}
	int result = pw_copy_proxy_fields(proxies, targetURL, 5.0,
		scheme, host, port, username, password, message);
	CFRelease(proxies);
	CFRelease(targetURL);
	return result;
}

typedef struct {
	int http_enabled, http_port, https_enabled, https_port, socks_enabled, socks_port;
	int pac_enabled, discovery_enabled, exclude_simple;
	char *http_host, *https_host, *socks_host, *pac_url, *exceptions;
} pw_proxy_settings;

static int pw_number(CFDictionaryRef settings, CFStringRef key) {
	CFNumberRef value = (CFNumberRef)CFDictionaryGetValue(settings, key);
	int out = 0;
	if (value != NULL && CFGetTypeID(value) == CFNumberGetTypeID()) CFNumberGetValue(value, kCFNumberIntType, &out);
	return out;
}

static char *pw_string(CFDictionaryRef settings, CFStringRef key) {
	CFStringRef value = (CFStringRef)CFDictionaryGetValue(settings, key);
	if (value == NULL || CFGetTypeID(value) != CFStringGetTypeID()) return NULL;
	return pw_copy_cfstring(value);
}

// Exceptions are joined with newlines, which no entry contains.
static char *pw_exceptions(CFDictionaryRef settings) {
	CFArrayRef list = (CFArrayRef)CFDictionaryGetValue(settings, CFSTR("ExceptionsList"));
	if (list == NULL || CFGetTypeID(list) != CFArrayGetTypeID()) return NULL;
	CFMutableStringRef joined = CFStringCreateMutable(kCFAllocatorDefault, 0);
	if (joined == NULL) return NULL;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFStringRef entry = (CFStringRef)CFArrayGetValueAtIndex(list, i);
		if (entry == NULL || CFGetTypeID(entry) != CFStringGetTypeID()) continue;
		if (CFStringGetLength(joined) > 0) CFStringAppend(joined, CFSTR("\n"));
		CFStringAppend(joined, entry);
	}
	char *out = pw_copy_cfstring(joined);
	CFRelease(joined);
	return out;
}

static int pw_read_proxy_settings(pw_proxy_settings *out) {
	CFDictionaryRef settings = CFNetworkCopySystemProxySettings();
	if (settings == NULL) return 0;
	out->http_enabled = pw_number(settings, CFSTR("HTTPEnable"));
	out->http_host = pw_string(settings, CFSTR("HTTPProxy"));
	out->http_port = pw_number(settings, CFSTR("HTTPPort"));
	out->https_enabled = pw_number(settings, CFSTR("HTTPSEnable"));
	out->https_host = pw_string(settings, CFSTR("HTTPSProxy"));
	out->https_port = pw_number(settings, CFSTR("HTTPSPort"));
	out->socks_enabled = pw_number(settings, CFSTR("SOCKSEnable"));
	out->socks_host = pw_string(settings, CFSTR("SOCKSProxy"));
	out->socks_port = pw_number(settings, CFSTR("SOCKSPort"));
	out->pac_enabled = pw_number(settings, CFSTR("ProxyAutoConfigEnable"));
	out->pac_url = pw_string(settings, CFSTR("ProxyAutoConfigURLString"));
	out->discovery_enabled = pw_number(settings, CFSTR("ProxyAutoDiscoveryEnable"));
	out->exclude_simple = pw_number(settings, CFSTR("ExcludeSimpleHostnames"));
	out->exceptions = pw_exceptions(settings);
	CFRelease(settings);
	return 1;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

const pacResolutionTimeout = 5 * time.Second

// Current returns the macOS system proxy configuration.
func Current() (Configuration, error) {
	var raw C.pw_proxy_settings
	if read := C.pw_read_proxy_settings(&raw); read == 0 {
		return Configuration{Source: SourceMacOS, Mode: ModeNone}, nil
	}
	defer func() {
		for _, value := range []*C.char{raw.http_host, raw.https_host, raw.socks_host, raw.pac_url, raw.exceptions} {
			if value != nil {
				C.free(unsafe.Pointer(value))
			}
		}
	}()
	settings := macSettings{
		HTTPEnabled: raw.http_enabled != 0, HTTPHost: goString(raw.http_host), HTTPPort: int(raw.http_port),
		HTTPSEnabled: raw.https_enabled != 0, HTTPSHost: goString(raw.https_host), HTTPSPort: int(raw.https_port),
		SOCKSEnabled: raw.socks_enabled != 0, SOCKSHost: goString(raw.socks_host), SOCKSPort: int(raw.socks_port),
		AutoConfigEnabled: raw.pac_enabled != 0, AutoConfigURL: goString(raw.pac_url),
		AutoDiscoveryEnabled:   raw.discovery_enabled != 0,
		ExcludeSimpleHostnames: raw.exclude_simple != 0,
	}
	if exceptions := goString(raw.exceptions); exceptions != "" {
		settings.Exceptions = strings.Split(exceptions, "\n")
	}
	return configurationFromMacSettings(settings), nil
}

func goString(value *C.char) string {
	if value == nil {
		return ""
	}
	return C.GoString(value)
}

// Lookup resolves system proxy settings for target.
func Lookup(target *url.URL) (*url.URL, error) {
	if target == nil {
		return nil, errors.New("system proxy target is required")
	}
	config, err := Current()
	if err != nil {
		return nil, err
	}
	return config.ProxyFor(target)
}

// evaluateMacOSAutomatic lets CFNetwork run the configured auto-configuration for target.
func evaluateMacOSAutomatic(target *url.URL) (*url.URL, error) {
	return lookupWith(func(fields *cProxyFields) C.int {
		raw := C.CString(target.String())
		defer C.free(unsafe.Pointer(raw))
		scheme := &fields.scheme
		host := &fields.host
		port := &fields.port
		username := &fields.username
		password := &fields.password
		message := &fields.message
		return C.pw_system_proxy(raw, C.double(pacResolutionTimeout.Seconds()),
			scheme, host, port, username, password, message)
	})
}

type cProxyFields struct {
	scheme   *C.char
	host     *C.char
	port     C.int
	username *C.char
	password *C.char
	message  *C.char
}

func lookupWith(call func(*cProxyFields) C.int) (*url.URL, error) {
	fields := cProxyFields{}
	defer fields.free()
	status := int(call(&fields))
	switch status {
	case 0:
		return nil, nil
	case 1:
		proxy := &url.URL{
			Scheme: C.GoString(fields.scheme),
			Host:   net.JoinHostPort(C.GoString(fields.host), strconv.Itoa(int(fields.port))),
		}
		if fields.username != nil {
			username := C.GoString(fields.username)
			if fields.password != nil {
				proxy.User = url.UserPassword(username, C.GoString(fields.password))
			} else {
				proxy.User = url.User(username)
			}
		}
		return proxy, nil
	default:
		message := "macOS proxy resolution failed"
		if fields.message != nil {
			message = C.GoString(fields.message)
		}
		return nil, fmt.Errorf("system proxy: %s", message)
	}
}

func (f *cProxyFields) free() {
	for _, value := range []*C.char{f.scheme, f.host, f.username, f.password, f.message} {
		if value != nil {
			C.free(unsafe.Pointer(value))
		}
	}
}

func lookupPACScript(script string, target *url.URL) (*url.URL, error) {
	return lookupWith(func(fields *cProxyFields) C.int {
		cScript := C.CString(script)
		cTarget := C.CString(target.String())
		defer C.free(unsafe.Pointer(cScript))
		defer C.free(unsafe.Pointer(cTarget))
		scheme := &fields.scheme
		host := &fields.host
		port := &fields.port
		username := &fields.username
		password := &fields.password
		message := &fields.message
		return C.pw_pac_script_proxy(cScript, cTarget,
			scheme, host, port, username, password, message)
	})
}
