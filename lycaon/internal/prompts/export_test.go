package prompts

import (
	"slices"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// DeclaredReaderFactScalarNames lists the declared scalar reader facts.
func DeclaredReaderFactScalarNames() []string {
	return slices.Clone(declaredReaderFactScalarNames)
}

// ReaderFactFamilies lists the declared reader-fact families.
func ReaderFactFamilies() []ReaderFactFamilySpec {
	return slices.Clone(readerFactFamilies)
}

// CheckOverlayTemplates runs the snapshot's overlay vocabulary check on one directory.
func CheckOverlayTemplates(eff *extpacks.EffectiveCatalog, overlayDir string) ([]string, error) {
	captured := map[string][]byte{}
	if err := capturePromptRoot(overlayDir, captured); err != nil {
		return nil, err
	}
	return checkCapturedOverlayTemplates(eff, captured)
}
