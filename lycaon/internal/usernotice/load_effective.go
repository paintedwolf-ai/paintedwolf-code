package usernotice

import (
	"fmt"
	"strings"
)

const userNoticeUnitPrefix = "host/user-notices/"

// EffectiveCatalogView supplies notice units without a package cycle.
type EffectiveCatalogView interface {
	LoadedUnitIDs() []string
	UnitContent(id string) (content []byte, packID string, ok bool)
}

// LoadEffectiveUserNotices merges effective notice units.
func LoadEffectiveUserNotices(eff EffectiveCatalogView) (*Config, error) {
	if eff == nil {
		return nil, fmt.Errorf("user notices: effective catalog required")
	}
	var defaults NoticeCopy
	haveDefaults := false
	notices := make(map[string]Entry)
	var failures []string
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, userNoticeUnitPrefix) {
			continue
		}
		content, _, ok := eff.UnitContent(id)
		if !ok {
			continue
		}
		stem := strings.TrimPrefix(id, userNoticeUnitPrefix)
		if stem == defaultsStem {
			parsed, err := ParseDefaults(content)
			if err != nil {
				failures = append(failures, id+": "+err.Error())
				continue
			}
			defaults = parsed
			haveDefaults = true
			continue
		}
		if !ValidNoticeCode(stem) {
			failures = append(failures, fmt.Sprintf("%s: invalid user notice stem %q", id, stem))
			continue
		}
		entry, err := ParseEntry(content)
		if err != nil {
			failures = append(failures, id+": "+err.Error())
			continue
		}
		if err := enforceNotificationFloor(stem, entry); err != nil {
			failures = append(failures, id+": "+err.Error())
			continue
		}
		notices[stem] = entry
	}
	if !haveDefaults {
		failures = append(failures, "host/user-notices/defaults required")
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("host/user-notices: %d load diagnostic(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return ConfigFromParts(defaults, notices)
}
