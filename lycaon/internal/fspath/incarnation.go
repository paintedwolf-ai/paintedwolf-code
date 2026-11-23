package fspath

import "strings"

func incarnationKey(identity string) (string, bool) {
	if identity == "" {
		return "", false
	}
	parts := strings.Split(identity, ":")
	var ino, discA, discB string
	switch len(parts) {
	case 4:
		ino, discA, discB = parts[1], parts[2], parts[3]
	case 5:
		ino, discA, discB = parts[2], parts[3], parts[4]
	default:
		return "", false
	}
	if ino == "" {
		return "", false
	}
	if strings.HasPrefix(discA, "h") {
		if len(discA) <= 1 || discB == "" {
			return "", false
		}
		return ino + ":" + discA + ":" + discB, true
	}
	if (discA == "" || discA == "0") && (discB == "" || discB == "0") {
		return "", false
	}
	return ino + ":" + discA + ":" + discB, true
}

// HasCreationDiscriminator reports whether identity carries birth time or an
// inode-generation handle rather than device and inode alone.
func HasCreationDiscriminator(identity string) bool {
	_, ok := incarnationKey(identity)
	return ok
}

// SameIncarnation reports whether recorded and current name the same entry
// creation across restarts. Bare device/inode pairs never match.
func SameIncarnation(recorded, current string) bool {
	recKey, recOK := incarnationKey(recorded)
	curKey, curOK := incarnationKey(current)
	return recOK && curOK && recKey == curKey
}
