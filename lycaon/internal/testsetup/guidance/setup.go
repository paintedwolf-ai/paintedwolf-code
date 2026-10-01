// Package guidancetestsetup installs bundled prompt guidance for test binaries.
package guidancetestsetup

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

// Install installs the bundled prompt-backed renderer.
func Install() {
	guidance.SetGuidanceRenderer(promptstest.BundledRenderer())
}
