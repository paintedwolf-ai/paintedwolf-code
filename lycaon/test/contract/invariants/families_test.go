package invariants_test

import (
	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/catalogload"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/outbox"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
	"github.com/lycaon/lycaon/test/contract/invariants/specsync"
	"github.com/lycaon/lycaon/test/contract/invariants/sqlschema"
	"github.com/lycaon/lycaon/test/contract/invariants/surfacematrix"
)

var families = []invariants.Family{
	forbiddencalls.Family,
	catalogload.Family,
	specsync.Family,
	sqlschema.Family,
	outbox.Family,
	surfacematrix.Family,
	promptrender.Family,
}
