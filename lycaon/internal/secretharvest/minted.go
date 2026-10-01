package secretharvest

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

const (
	// minMintedRunes is the credential-token length floor.
	minMintedRunes = 20
	// maxMintedValues bounds one output's contribution.
	maxMintedValues = 16
)

// MintedCandidates extracts credential-shaped tokens from flagged output.
func MintedCandidates(output string) []string {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, maxMintedValues)
	for _, token := range strings.FieldsFunc(output, isTokenBoundary) {
		token = strings.Trim(token, ".,;:'\"`")
		if !looksMinted(token) {
			continue
		}
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
		if len(out) == maxMintedValues {
			return out
		}
	}
	return out
}

// isTokenBoundary retains common encoded-token characters.
func isTokenBoundary(r rune) bool {
	switch {
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return false
	case r == '-' || r == '_' || r == '.' || r == '+' || r == '/' || r == '=':
		return false
	default:
		return true
	}
}

// looksMinted requires one long alphanumeric run within the whole token.
func looksMinted(token string) bool {
	if utf8.RuneCountInString(token) < minMintedRunes {
		return false
	}
	run, digits, longest, longestDigits := 0, 0, 0, 0
	for _, r := range token + "." {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			run++
			if unicode.IsDigit(r) {
				digits++
			}
			continue
		}
		if run > longest {
			longest, longestDigits = run, digits
		}
		run, digits = 0, 0
	}
	// Digits distinguish encoded material from prose.
	return longest >= minMintedRunes && longestDigits > 0
}

// RememberMinted records a flagged call's output values under the rule that
// established their provenance.
func RememberMinted(rule, title, tool, output string) []secretmatch.Remembered {
	candidates := MintedCandidates(output)
	if len(candidates) == 0 {
		return nil
	}
	out := make([]secretmatch.Remembered, 0, len(candidates))
	for _, value := range candidates {
		out = append(out, secretmatch.Remembered{
			Secret: value,
			Name:   tool,
			Origin: "minted by " + tool,
			RuleID: rule,
			Title:  title,
			Source: secretmatch.SourceRememberedMatch,
		})
	}
	return out
}
