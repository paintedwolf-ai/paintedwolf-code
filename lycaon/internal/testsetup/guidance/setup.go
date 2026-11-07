// Package guidancetestsetup installs bundled prompt guidance for test binaries.
package guidancetestsetup

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

// Install installs the bundled prompt-backed renderer and resolves the
// guidance catalog once in real time.
//
// The bundled renderer reads the catalog from device state on first use,
// taking a cross-process extension lock with a wall-clock retry deadline.
// Parallel test binaries share one config root, and a first render inside a
// testing/synctest bubble runs that deadline on the bubble's fake clock: the
// wait collapses, transient contention fails the render, and the bubble
// deadlocks. Resolving here, before any test runs, makes later renders cache
// hits that take no lock.
func Install() {
	guidance.SetGuidanceRenderer(promptstest.BundledRenderer())
	if _, err := guidance.RenderTodayLine(context.Background(), time.Now(), ""); err != nil {
		panic("guidancetestsetup: resolve guidance catalog: " + err.Error())
	}
}
