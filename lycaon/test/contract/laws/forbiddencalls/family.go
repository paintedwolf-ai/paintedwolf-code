// Package forbiddencalls holds the single-door laws: effects reached only
// through their declared function, on the parsed Go corpus.
package forbiddencalls

import (
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// Family is the forbidden-calls rule table.
var Family = laws.Family{Name: "forbidden-calls", Rules: slices.Concat(
	testInfrastructure,
	agentDoors,
	approvals,
	concurrency,
	debugCapture,
	guidanceEmission,
	isolation,
	authorities,
	projectRoots,
	prose,
	coordinator,
	person,
	promptReader,
	runVars,
	surveyTools,
	toolIngest,
	userText,
	projectWalk,
	confinement,
	bootRecovery,
	debugSwitch,
	templates,
	catalogReach,
	coordinatorWaits,
	transcriptPublish,
	evidenceSubstrate,
)}
