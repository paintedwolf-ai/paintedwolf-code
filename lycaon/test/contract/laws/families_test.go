package laws_test

import (
	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/catalogload"
	"github.com/lycaon/lycaon/test/contract/laws/closure"
	"github.com/lycaon/lycaon/test/contract/laws/densource"
	"github.com/lycaon/lycaon/test/contract/laws/enumclosure"
	"github.com/lycaon/lycaon/test/contract/laws/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/laws/outbox"
	"github.com/lycaon/lycaon/test/contract/laws/promptrender"
	"github.com/lycaon/lycaon/test/contract/laws/specsync"
	"github.com/lycaon/lycaon/test/contract/laws/sqlschema"
	"github.com/lycaon/lycaon/test/contract/laws/surfacematrix"
	"github.com/lycaon/lycaon/test/contract/laws/vendorpin"
)

var families = []laws.Family{
	forbiddencalls.Family,
	catalogload.Family,
	specsync.Family,
	sqlschema.Family,
	outbox.Family,
	surfacematrix.Family,
	promptrender.Family,
	vendorpin.Family,
	enumclosure.Family,
	densource.Family,
	closure.Family,
}
