package systemproxy

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	gsettingsSchema   = "org.gnome.system.proxy"
	maxGSettingsBytes = 64 << 10
	maxGSettingsLines = 256
)

// gsettingsValue is one typed value from GVariant text notation.
type gsettingsValue struct {
	text    string
	boolean bool
	number  int
	list    []string
	kind    byte // 's', 'b', 'i' or 'a'
}

// parseGSettingsDump reads `gsettings list-recursively` output for the proxy
// schema and its children into a desktop configuration.
func parseGSettingsDump(raw []byte) (desktopConfiguration, error) {
	if len(raw) > maxGSettingsBytes {
		return desktopConfiguration{}, errors.New("desktop proxy settings exceed their size limit")
	}
	values := make(map[string]gsettingsValue, 32)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > maxGSettingsLines {
		return desktopConfiguration{}, errors.New("desktop proxy settings exceed their line limit")
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 || !strings.HasPrefix(fields[0], gsettingsSchema) {
			return desktopConfiguration{}, fmt.Errorf("desktop proxy settings line is not a schema entry: %q", line)
		}
		value, err := parseGVariant(fields[2])
		if err != nil {
			return desktopConfiguration{}, fmt.Errorf("desktop proxy setting %s %s: %w", fields[0], fields[1], err)
		}
		values[strings.TrimPrefix(fields[0], gsettingsSchema)+" "+fields[1]] = value
	}
	config := desktopConfiguration{Source: SourceGSettings}
	switch mode := values[" mode"].text; mode {
	case "none", "":
		config.Mode = modeNone
		return config, nil
	case "manual":
		config.Mode = modeManual
	case "auto":
		config.Mode = modeAuto
		config.Script = values[" autoconfig-url"].text
		return config, nil
	default:
		return desktopConfiguration{}, fmt.Errorf("desktop proxy mode %q is unknown", mode)
	}
	config.SameProxy = values[" use-same-proxy"].boolean
	config.Bypass = values[" ignore-hosts"].list
	var user *url.Userinfo
	if values[".http use-authentication"].boolean {
		name := values[".http authentication-user"].text
		if password := values[".http authentication-password"].text; password != "" {
			user = url.UserPassword(name, password)
		} else if name != "" {
			user = url.User(name)
		}
	}
	config.HTTP = manualProxy("http", values[".http host"].text, values[".http port"].number, user)
	config.HTTPS = manualProxy("http", values[".https host"].text, values[".https port"].number, nil)
	config.SOCKS = manualProxy("socks5", values[".socks host"].text, values[".socks port"].number, nil)
	return config, nil
}

// parseGVariant accepts the scalar and string-array notation the proxy schema uses.
func parseGVariant(text string) (gsettingsValue, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "true" || text == "false":
		return gsettingsValue{kind: 'b', boolean: text == "true"}, nil
	case strings.HasPrefix(text, "'") || strings.HasPrefix(text, "\""):
		value, rest, err := parseGVariantString(text)
		if err != nil || strings.TrimSpace(rest) != "" {
			return gsettingsValue{}, errors.New("invalid string notation")
		}
		return gsettingsValue{kind: 's', text: value}, nil
	case strings.HasPrefix(text, "@as ") || strings.HasPrefix(text, "["):
		list, err := parseGVariantStringArray(strings.TrimSpace(strings.TrimPrefix(text, "@as ")))
		if err != nil {
			return gsettingsValue{}, err
		}
		return gsettingsValue{kind: 'a', list: list}, nil
	}
	// Integer notation may carry a type annotation such as `uint16 8080`.
	if before, after, ok := strings.Cut(text, " "); ok {
		text = after
		switch before {
		case "byte", "int16", "uint16", "int32", "uint32", "int64", "uint64":
		default:
			return gsettingsValue{}, errors.New("invalid annotated notation")
		}
	}
	number, err := strconv.Atoi(text)
	if err != nil {
		return gsettingsValue{}, errors.New("invalid integer notation")
	}
	return gsettingsValue{kind: 'i', number: number}, nil
}

func parseGVariantString(text string) (value, rest string, err error) {
	quote := text[0]
	var out strings.Builder
	for i := 1; i < len(text); i++ {
		switch c := text[i]; c {
		case '\\':
			if i+1 >= len(text) {
				return "", "", errors.New("unterminated escape")
			}
			i++
			switch text[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case '\\', '\'', '"':
				out.WriteByte(text[i])
			default:
				return "", "", errors.New("unsupported escape")
			}
		case quote:
			return out.String(), text[i+1:], nil
		default:
			out.WriteByte(c)
		}
	}
	return "", "", errors.New("unterminated string")
}

func parseGVariantStringArray(text string) ([]string, error) {
	if !strings.HasPrefix(text, "[") || !strings.HasSuffix(text, "]") {
		return nil, errors.New("invalid array notation")
	}
	body := strings.TrimSpace(text[1 : len(text)-1])
	list := []string{}
	for body != "" {
		if body[0] != '\'' && body[0] != '"' {
			return nil, errors.New("array holds a non-string element")
		}
		value, rest, err := parseGVariantString(body)
		if err != nil {
			return nil, err
		}
		list = append(list, value)
		body = strings.TrimSpace(rest)
		if body == "" {
			break
		}
		if body[0] != ',' {
			return nil, errors.New("array elements are not comma separated")
		}
		body = strings.TrimSpace(body[1:])
	}
	return list, nil
}
