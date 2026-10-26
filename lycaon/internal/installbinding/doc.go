// Package installbinding keeps a running engine on the package generation it
// started from. A package upgrade can replace the installed tree while an old
// engine still runs; launching a shipped helper from the replacement would mix
// old engine and new helper bytes. Every launch of an installed executable is
// checked against the manifest captured at startup and refused with a restart
// outcome once the installation has changed.
//
// The protected launch (#1118) runs the engine from an immutable generation; it
// installs a binding whose installation can no longer change underneath it.
package installbinding

import "errors"

// ErrReplaced reports that the installation changed after this engine started.
var ErrReplaced = errors.New("the Painted Wolf Code installation was updated while it was running; restart Painted Wolf Code to use the new version")
