package detectionpack

import (
	"fmt"
	"sort"

	"github.com/lycaon/lycaon/config"
)

// CredentialStorePackID names the pack that catalogues credential stores. Any pack may
// match on TargetFile; only this one answers "what is a credential store".
const CredentialStorePackID = "credential-stores" // #nosec G101 -- a pack directory name, not a secret

// BundledCredentialStorePaths returns the home-relative store paths the shipped
// credential-stores pack names.
//
// Only the embedded pack defines this fail-closed boundary input.
func BundledCredentialStorePaths() ([]string, error) {
	files := bundledPackFiles(config.DetectionPacksDir.Join(CredentialStorePackID))
	pack, warnings := ParsePack(files)
	if pack == nil {
		return nil, fmt.Errorf("bundled %s pack did not load: %v", CredentialStorePackID, warnings)
	}
	paths := targetFilePathsFromRules(pack.Rules)
	if len(paths) == 0 {
		return nil, fmt.Errorf("bundled %s pack names no TargetFile paths", CredentialStorePackID)
	}
	return paths, nil
}

// CredentialStorePaths returns the store paths this matcher's credential-stores rules
// name, including any the person added through an overlay — at any severity level.
// It feeds a hard write-deny floor, so it skips the ask-only severity filter: a
// low-severity overlay rule still extends the floor.
func (m *Matcher) CredentialStorePaths() []string {
	if m == nil {
		return nil
	}
	return targetFilePathsFromRules(m.credentialStoreRules)
}

func targetFilePathsFromRules(rules []Rule) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, rule := range rules {
		if !rule.Supported {
			continue
		}
		for _, value := range rule.FieldLiterals("TargetFile") {
			if _, dup := seen[value]; dup {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
