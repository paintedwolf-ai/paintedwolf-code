package fseffect

import "errors"

// ErrPredicateChanged refuses a metadata/access observation whose exact target
// changed during the probe. It is not a boolean false or a reusable grant.
var ErrPredicateChanged = errors.New("filesystem predicate target changed")
