//go:build !linux

package linuxsandbox

import "errors"

// PerformerFlag, as a binary's first argument, starts the Linux sandbox's
// performer, which exists only there.
const PerformerFlag = "--create-performer"

// ServePerformer refuses: only the Linux sandbox has a performer.
func ServePerformer() error {
	return errors.New("the sandbox performer runs only on Linux")
}
