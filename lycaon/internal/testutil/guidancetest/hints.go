package guidancetest

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// ValidateHints checks copy bindings, scenarios and retired-code successors.
func ValidateHints(cfg *guidance.HintConfig) error {
	if cfg == nil || len(cfg.HintCodes) == 0 {
		return fmt.Errorf("hint_codes required")
	}
	var errs []string
	for code, entry := range cfg.HintCodes {
		if err := validateHintEntry(code, entry); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for code, entry := range cfg.HintCodes {
		if err := validateHintSuccessors(code, entry, cfg.HintCodes); err != nil {
			errs = append(errs, err.Error())
		}
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return fmt.Errorf("hint-codes:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

var hintCodeRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func validateHintSuccessors(code string, entry guidance.HintEntry, codes map[string]guidance.HintEntry) error {
	successor := strings.TrimSpace(entry.Successor)
	// Code successors must exist in this registry.
	if hintCodeRE.MatchString(successor) {
		if successor == code {
			return fmt.Errorf("%s: successor must not reference itself", code)
		}
		if _, ok := codes[successor]; !ok {
			return fmt.Errorf("%s: successor %q is not a known hint code", code, successor)
		}
	}
	if strings.TrimSpace(entry.Status) == "deprecated" && successor == "" {
		return fmt.Errorf("%s: deprecated hint requires x-paintedwolf-successor naming the replacement", code)
	}
	return nil
}

// CopyOnlyEmit reports whether a unit's emit channel carries informational
// copy rather than a rejection card, so it needs no rejection fields or
// rehearsal scenarios.
func CopyOnlyEmit(emit string) bool {
	emit = strings.TrimSpace(emit)
	return emit == guidance.EmitBanner || strings.HasPrefix(emit, guidance.EmitUIPrefix) || strings.HasPrefix(emit, guidance.EmitInjectPrefix)
}

func validateHintEntry(code string, entry guidance.HintEntry) error {
	copyOnly := CopyOnlyEmit(entry.Emit)
	view := guidance.NormalizeRejectCodeView(code, entry)
	if len(entry.Scenarios) == 0 && !copyOnly && entry.Kind != "hint" && entry.Severity != "warning" {
		return fmt.Errorf("%s: scenarios required (>=1)", code)
	}
	for _, sc := range entry.Scenarios {
		if strings.TrimSpace(sc.ID) == "" {
			return fmt.Errorf("%s: scenario id required", code)
		}
	}
	if err := validateCopyFields(code, view); err != nil {
		return err
	}
	return nil
}

func validateCopyFields(code string, view guidance.RejectCodeView) error {
	for _, field := range []struct {
		name, val string
	}{
		{"what", view.What},
		{"cause", view.Cause},
		{"why", view.Why},
		{"fix", view.Fix},
		{"instead", view.Instead},
	} {
		val := strings.TrimSpace(field.val)
		if val == "" {
			continue
		}
		if !strings.Contains(val, "{{") && !strings.Contains(val, "{%") {
			continue
		}
		if _, err := oarcore.ParseCopy(val); err != nil {
			return fmt.Errorf("%s: %s copy binding: %w", code, field.name, err)
		}
	}
	return nil
}
