package systemproxy

import (
	"fmt"
	"os"
	"strings"
)

type kioEntries struct {
	values map[string]string
	locked map[string]bool
	found  bool
	frozen bool
}

// Group locks are published after parsing their defining file, as in KConfig.
func (g *kioEntries) merge(raw string, lookupEnv func(string) (string, bool)) (bool, error) {
	inside, groupLock, fileLock := false, false, false
	for index, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, flags, err := kioName(line)
			if err != nil {
				return false, fmt.Errorf("line %d: %w", index+1, err)
			}
			if name == "$i" {
				fileLock = true
				inside = false
				continue
			}
			inside = name == kioProxyGroup && !g.frozen
			if inside {
				g.found = true
				groupLock = groupLock || flags["i"] || fileLock
			}
			continue
		}
		if !inside {
			continue
		}
		if err := g.apply(line, groupLock, lookupEnv); err != nil {
			return false, fmt.Errorf("line %d: %w", index+1, err)
		}
	}
	g.frozen = g.frozen || groupLock
	return fileLock, nil
}

func (g *kioEntries) apply(line string, groupLock bool, lookupEnv func(string) (string, bool)) error {
	key, value, hasValue := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	base, suffix, suffixed := strings.Cut(key, "[")
	key = "[" + base + "]"
	if suffixed {
		key += "[" + suffix
	}
	name, flags, err := kioName(key)
	if err != nil {
		return err
	}
	if strings.Contains(name, "\x1d") {
		return fmt.Errorf("localized KDE proxy entries are unsupported")
	}
	if g.locked[name] {
		return nil
	}
	if flags["d"] {
		delete(g.values, name)
	} else {
		if !hasValue {
			return fmt.Errorf("KDE proxy entry has no value")
		}
		value, err = kioString(strings.TrimSpace(value))
		if err != nil {
			return err
		}
		if flags["e"] {
			value = kioExpand(value, lookupEnv)
		}
		g.values[name] = value
	}
	g.locked[name] = flags["i"] || groupLock
	return nil
}

func kioName(raw string) (string, map[string]bool, error) {
	parts := []string{}
	for raw != "" {
		if !strings.HasPrefix(raw, "[") {
			return "", nil, fmt.Errorf("invalid KDE configuration name")
		}
		part, rest, ok := strings.Cut(raw[1:], "]")
		if !ok {
			return "", nil, fmt.Errorf("unterminated KDE configuration name")
		}
		parts, raw = append(parts, part), rest
	}
	flags := map[string]bool{}
	for _, suffix := range parts[1:] {
		if !strings.HasPrefix(suffix, "$") {
			parts[0] += "\x1d" + suffix
			continue
		}
		for _, flag := range suffix[1:] {
			flags[string(flag)] = true
		}
	}
	return parts[0], flags, nil
}

func kioString(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out.WriteByte(raw[i])
			continue
		}
		i++
		if i == len(raw) {
			return "", fmt.Errorf("unterminated KDE configuration escape")
		}
		switch raw[i] {
		case 's':
			out.WriteByte(' ')
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case '\\':
			out.WriteByte('\\')
		default:
			return "", fmt.Errorf("unsupported KDE configuration escape")
		}
	}
	return out.String(), nil
}

func kioExpand(raw string, lookupEnv func(string) (string, bool)) string {
	// KConfig treats a doubled dollar as one literal dollar.
	parts := strings.Split(raw, "$$")
	for i, part := range parts {
		parts[i] = os.Expand(part, func(key string) string { value, _ := lookupEnv(key); return value })
	}
	return strings.Join(parts, "$")
}
