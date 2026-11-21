package detectionpack

import (
	"fmt"

	"github.com/lycaon/lycaon/config"
)

// KeyMaterialPackID names the pack that catalogues private keys and the stores
// that decrypt them. Any pack may match on TargetFile; only this one answers
// "what is key material".
const KeyMaterialPackID = "key-material"

// BundledKeyMaterialPaths returns the home-relative paths the shipped
// key-material pack names. The list is the write floor: a path here is refused
// with no grant offered, confine refuses a project root that would swallow one,
// a load failure is a boot failure, and overlays do not extend it.
func BundledKeyMaterialPaths() ([]string, error) {
	return bundledFloorPaths(KeyMaterialPackID)
}

// bundledFloorPaths loads a write-floor pack whole: a rule that does not parse
// would silently narrow the floor, so any load warning is an error.
func bundledFloorPaths(packID string) ([]string, error) {
	pack, warnings := ParsePack(bundledPackFiles(config.DetectionPacksDir.Join(packID)))
	if pack == nil || len(warnings) > 0 {
		return nil, fmt.Errorf("bundled %s pack did not load: %v", packID, warnings)
	}
	paths := targetFilePathsFromRules(pack.Rules)
	if len(paths) == 0 {
		return nil, fmt.Errorf("bundled %s pack names no TargetFile paths", packID)
	}
	return paths, nil
}
