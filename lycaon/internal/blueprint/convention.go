package blueprint

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// BlueprintsDir is the project-relative convention root for governing markdown.
func BlueprintsDir() string { return settingsoverlay.Rel("blueprints") }

// ConventionPath places a filename under the active blueprint root.
func ConventionPath(file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	return BlueprintsDir() + "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")
}

// FileMode and DirMode are project-visible blueprint permissions.
const (
	FileMode = 0o644
	DirMode  = 0o755
)

// MAX_PROJECT_BLUEPRINTS caps list responses (newest-first).
const MAX_PROJECT_BLUEPRINTS = 100

// MaxBlueprintTitleLen is the display-title cap for rename.
const MaxBlueprintTitleLen = 120

// ErrInvalidBlueprintTitle is returned when a display-title rename fails validation.
var ErrInvalidBlueprintTitle = errors.New("invalid blueprint title")

// ErrInvalidBlueprintPath identifies a path outside the blueprint convention.
var ErrInvalidBlueprintPath = errors.New("invalid blueprint path")

// NormalizeBlueprintDisplayTitle trims and validates a blueprint title.
func NormalizeBlueprintDisplayTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", ErrInvalidBlueprintTitle
	}
	if utf8.RuneCountInString(title) > MaxBlueprintTitleLen {
		return "", ErrInvalidBlueprintTitle
	}
	if strings.ContainsAny(title, "\n\r\x00") {
		return "", ErrInvalidBlueprintTitle
	}
	return title, nil
}

// ValidateConventionPath validates a project-relative blueprint markdown path.
func ValidateConventionPath(rel string) error {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" {
		return fmt.Errorf("%w: path required", ErrInvalidBlueprintPath)
	}
	if strings.Contains(rel, "\\") {
		rel = filepath.ToSlash(rel)
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, ":") {
		return fmt.Errorf("%w: path must be project-relative", ErrInvalidBlueprintPath)
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == ".." || p == "" {
			return fmt.Errorf("%w: path must not contain empty or .. segments", ErrInvalidBlueprintPath)
		}
	}
	prefix := BlueprintsDir() + "/"
	if rel != BlueprintsDir() && !strings.HasPrefix(rel, prefix) {
		return fmt.Errorf("%w: path must be under %s/", ErrInvalidBlueprintPath, BlueprintsDir())
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return fmt.Errorf("%w: path must end in .md", ErrInvalidBlueprintPath)
	}
	if rel == BlueprintsDir()+".md" || rel == BlueprintsDir() {
		return fmt.Errorf("%w: path must be a file under %s/", ErrInvalidBlueprintPath, BlueprintsDir())
	}
	return nil
}

// SlugTitle turns a title into a filesystem-safe slug fragment.
func SlugTitle(title string) string {
	title = strings.TrimSpace(strings.ToLower(title))
	if title == "" {
		return "blueprint"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "blueprint"
	}
	for len(out) > 48 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return strings.Trim(out, "-")
}

const mintDayLayout = "2006-01-02"

// MintDay is the UTC calendar day used when a blueprint is still unnamed.
func MintDay(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC().Format(mintDayLayout)
}

// MintStem is the filename stem: a slug when the title is declared, otherwise the mint day.
func MintStem(title string, now time.Time) string {
	if blueprintfile.IsPlaceholderTitle(title) {
		return MintDay(now)
	}
	return SlugTitle(title)
}

// ConventionFileName builds stem.md or stem-N.md for N > 1.
func ConventionFileName(stem string, n int) string {
	stem = strings.TrimSpace(stem)
	if stem == "" {
		stem = MintDay(time.Time{})
	}
	if n <= 1 {
		return stem + ".md"
	}
	return stem + "-" + strconv.Itoa(n) + ".md"
}

// MintUniquePath picks a free convention path for title.
func MintUniquePath(title string, now time.Time, taken func(string) bool) string {
	stem := MintStem(title, now)
	for n := 1; n < 10000; n++ {
		path := ConventionPath(ConventionFileName(stem, n))
		if taken == nil || !taken(path) {
			return path
		}
	}
	return ConventionPath(ConventionFileName(stem+"-"+MintDay(now), 1))
}

// PathFileStem is the basename without the .md suffix.
func PathFileStem(path string) string {
	base := filepath.Base(filepath.ToSlash(strings.TrimSpace(path)))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// IsProvisionalPath reports whether the filename is still a host mint
// (calendar day, day-N, or the exact names blueprint/plan).
func IsProvisionalPath(path string) bool {
	return isProvisionalStem(PathFileStem(path))
}

func isProvisionalStem(stem string) bool {
	stem = strings.TrimSpace(stem)
	if stem == "" {
		return true
	}
	if blueprintfile.IsPlaceholderTitle(stem) || isMintDayStem(stem) {
		return true
	}
	// day-N is provisional; a declared slug like blueprint-2 is not.
	i := strings.LastIndex(stem, "-")
	if i <= 0 {
		return false
	}
	prefix, suffix := stem[:i], stem[i+1:]
	if !isAllDigits(suffix) {
		return false
	}
	return isMintDayStem(prefix)
}

func isMintDayStem(stem string) bool {
	if len(stem) != len(mintDayLayout) {
		return false
	}
	_, err := time.Parse(mintDayLayout, stem)
	return err == nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
