package laws_test

import (
	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/catalogload"
	"github.com/lycaon/lycaon/test/contract/laws/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/laws/outbox"
	"github.com/lycaon/lycaon/test/contract/laws/specsync"
	"github.com/lycaon/lycaon/test/contract/laws/sqlschema"
)

var families = []laws.Family{
	forbiddencalls.Family,
	catalogload.Family,
	specsync.Family,
	sqlschema.Family,
	outbox.Family,
}
