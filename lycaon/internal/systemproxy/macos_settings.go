package systemproxy

// macSettings are the CFNetwork system proxy settings the host reads.
type macSettings struct {
	HTTPEnabled  bool
	HTTPHost     string
	HTTPPort     int
	HTTPSEnabled bool
	HTTPSHost    string
	HTTPSPort    int
	SOCKSEnabled bool
	SOCKSHost    string
	SOCKSPort    int

	AutoConfigEnabled    bool
	AutoConfigURL        string
	AutoDiscoveryEnabled bool

	Exceptions             []string
	ExcludeSimpleHostnames bool
}

// configurationFromMacSettings maps the settings; auto-configuration takes
// precedence because CFNetwork consults it before the fixed proxies.
func configurationFromMacSettings(settings macSettings) Configuration {
	config := Configuration{Source: SourceMacOS, Mode: ModeNone}
	if settings.AutoConfigEnabled || settings.AutoDiscoveryEnabled {
		config.Mode = ModeAuto
		if settings.AutoConfigEnabled {
			config.Script = settings.AutoConfigURL
		}
		return config
	}
	if settings.HTTPEnabled {
		config.HTTP = manualProxy("http", settings.HTTPHost, settings.HTTPPort, nil)
	}
	if settings.HTTPSEnabled {
		config.HTTPS = manualProxy("http", settings.HTTPSHost, settings.HTTPSPort, nil)
	}
	if settings.SOCKSEnabled {
		config.SOCKS = manualProxy("socks5", settings.SOCKSHost, settings.SOCKSPort, nil)
	}
	if config.HTTP == nil && config.HTTPS == nil && config.SOCKS == nil {
		return config
	}
	config.Mode = ModeManual
	config.Exceptions = desktopExceptions(settings.Exceptions)
	if settings.ExcludeSimpleHostnames {
		config.Exceptions = append(config.Exceptions, Exception{Kind: ExceptionSimpleHostnames, Raw: "ExcludeSimpleHostnames"})
	}
	return config
}
