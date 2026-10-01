package scan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const CategoryAll = "all"

var ErrInvalidScanCategory = errors.New("invalid scan category")

// PackScanCategories are the broad host-scan categories used when agents request "all".
func PackScanCategories() []api.ScanCategory {
	return []api.ScanCategory{
		api.ScanCategorySecurity,
		api.ScanCategorySAST,
		api.ScanCategorySecret,
		api.ScanCategorySCA,
	}
}

// ResolveScanCategories normalizes agent/API category requests.
// Empty input defaults to security+secret. "all" expands to PackScanCategories.
func ResolveScanCategories(in []api.ScanCategory) ([]api.ScanCategory, error) {
	if len(in) == 0 {
		return []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}, nil
	}
	wantAll := false
	out := make([]api.ScanCategory, 0, len(in))
	for _, c := range in {
		s := strings.TrimSpace(string(c))
		if s == "" {
			continue
		}
		if s == CategoryAll {
			wantAll = true
			continue
		}
		cat := api.ScanCategory(s)
		if !api.IsKnownScanCategory(cat) {
			return nil, fmt.Errorf("%w %q (valid: %s, or all)", ErrInvalidScanCategory, s, api.ScanCategoryList())
		}
		out = append(out, cat)
	}
	if wantAll {
		return normalizeCategories(PackScanCategories()), nil
	}
	if len(out) == 0 {
		return []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}, nil
	}
	return normalizeCategories(out), nil
}

// ParseScanCategoryArgs parses scan_pack (or API) category arrays from JSON tool args.
func ParseScanCategoryArgs(raw any) ([]api.ScanCategory, error) {
	return ResolveScanCategories(parseCategoryArgs(raw))
}

func parseCategoryArgs(raw any) []api.ScanCategory {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]api.ScanCategory, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, api.ScanCategory(strings.TrimSpace(s)))
		}
	}
	return out
}

// SameCategories compares category sets as multisets, ignoring order.
func SameCategories(left, right []api.ScanCategory) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[api.ScanCategory]int, len(left))
	for _, category := range left {
		seen[category]++
	}
	for _, category := range right {
		seen[category]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}
