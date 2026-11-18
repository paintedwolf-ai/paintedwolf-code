package invariants_test

import (
	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/catalogload"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

var families = []invariants.Family{
	forbiddencalls.Family,
	catalogload.Family,
}
