package project

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/projectroot"
)

var slugRepeatDash = regexp.MustCompile(`-+`)

// MaxRootDisplayLabelRunes limits root labels.
const MaxRootDisplayLabelRunes = 64

// ErrInvalidRootLabel marks an invalid root label.
var ErrInvalidRootLabel = errors.New("invalid root label")

// ErrDuplicateRootLabel marks a case-insensitive collision.
var ErrDuplicateRootLabel = errors.New("duplicate root label")

// ErrReservedRootLabel marks a label that names a host namespace such as
// @scratch. It is an invalid label, so refusals map as ErrInvalidRootLabel.
var ErrReservedRootLabel = fmt.Errorf("%w: reserved for a host namespace", ErrInvalidRootLabel)

// NormalizeRootDisplayLabel validates an @label token. It is the one label
// rule: attach, rename, and every stored project read apply it.
func NormalizeRootDisplayLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" || label == "." || label == ".." {
		return "", ErrInvalidRootLabel
	}
	if utf8.RuneCountInString(label) > MaxRootDisplayLabelRunes {
		return "", ErrInvalidRootLabel
	}
	if strings.ContainsAny(label, "/\\") {
		return "", ErrInvalidRootLabel
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", ErrInvalidRootLabel
		}
	}
	if strings.HasPrefix(label, "@") {
		return "", ErrInvalidRootLabel
	}
	if projectroot.IsVirtualRootLabel(label) {
		return "", ErrReservedRootLabel
	}
	return label, nil
}

// rootLabelTaken checks sibling labels case-insensitively.
func rootLabelTaken(roots []Root, label, excludeRootID string) bool {
	for _, r := range roots {
		if r.ID == excludeRootID {
			continue
		}
		if strings.EqualFold(r.Label, label) {
			return true
		}
	}
	return false
}

// SlugProjectName returns a search-safe project slug.
func SlugProjectName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return "root"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := slugRepeatDash.ReplaceAllString(strings.Trim(b.String(), "-"), "-")
	if out == "" {
		return "root"
	}
	return out
}

func deriveUniqueRootLabel(existing []string, path string) string {
	base := filepath.Base(filepath.Clean(path))
	candidate := rootLabelFromBasename(base)
	if !rootLabelInUse(existing, candidate) {
		return candidate
	}
	for i := 2; ; i++ {
		next := rootLabelWithSuffix(candidate, "-"+strconv.Itoa(i))
		if !rootLabelInUse(existing, next) {
			return next
		}
	}
}

func rootLabelInUse(existing []string, candidate string) bool {
	if projectroot.IsVirtualRootLabel(candidate) {
		return true
	}
	for _, label := range existing {
		if strings.EqualFold(label, candidate) {
			return true
		}
	}
	return false
}

func rootLabelFromBasename(basename string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(basename) {
		switch {
		case unicode.IsControl(r), r == '/', r == '\\':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	label := strings.TrimSpace(strings.TrimLeft(b.String(), "@"))
	if label == "" || label == "." || label == ".." {
		label = "root"
	}
	return truncateRootLabel(label, MaxRootDisplayLabelRunes)
}

func rootLabelWithSuffix(label, suffix string) string {
	limit := MaxRootDisplayLabelRunes - utf8.RuneCountInString(suffix)
	return truncateRootLabel(label, limit) + suffix
}

func truncateRootLabel(label string, limit int) string {
	runes := []rune(label)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}
