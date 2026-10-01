//go:build gts_parsercorephase0 && !gts_no_parsercorephase0

package gotreesitter

import (
	"encoding/json"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

type diagnosticParserCoreFrontierObserverPayloadForTest struct {
	Frontier      json.RawMessage        `json:"frontier"`
	HeaderHeads   []core.Head            `json:"header_heads"`
	HeaderRefs    [][]core.DropCohortRef `json:"header_refs"`
	DropIndices   []int                  `json:"drop_indices"`
	ElectionIndex int                    `json:"election_index"`
}

func diagnosticParserCoreFrontierObserverPayload(
	scheduler *diagnosticParserCoreGenericScheduler,
	owner core.SchedulerTransactionToken,
	dropIndices []int,
) []byte {
	if scheduler == nil || scheduler.compact == nil {
		return []byte(`{"frontier":{"schema":"gts-drop-cohort-frontier/v1"}}`)
	}
	heads := make([]core.Head, len(scheduler.headers))
	refs := make([][]core.DropCohortRef, len(scheduler.headers))
	for index, header := range scheduler.headers {
		heads[index] = header.head
		refs[index] = make([]core.DropCohortRef, 0, header.dropCohortRefs.Len())
		for refIndex := 0; refIndex < header.dropCohortRefs.Len(); refIndex++ {
			ref, ok := scheduler.compact.DropCohortRefAtOwned(owner, header.dropCohortRefs, refIndex)
			if !ok {
				return []byte(`{"frontier":{"schema":"gts-drop-cohort-frontier/v1"}}`)
			}
			refs[index] = append(refs[index], ref)
		}
	}
	frontier := scheduler.compact.DiagnosticDropCohortFrontierSnapshotOwnedForTest(owner)
	payload, err := json.Marshal(diagnosticParserCoreFrontierObserverPayloadForTest{
		Frontier:      frontier,
		HeaderHeads:   heads,
		HeaderRefs:    refs,
		DropIndices:   append([]int(nil), dropIndices...),
		ElectionIndex: scheduler.electionIndex,
	})
	if err != nil {
		return []byte(`{"frontier":{"schema":"gts-drop-cohort-frontier/v1"}}`)
	}
	return payload
}

// DiagnosticParserCoreFrontierObserverForTest receives one latest frontier
// snapshot after the producer seals it and before it publishes header state.
// The hook exists only in the focused parser-core test build.
type DiagnosticParserCoreFrontierObserverForTest func([]byte)

// DiagnosticParseWithDropCohortFrontierObserverForTest runs a separate probe
// parser with D3 certificates and D6a frontier recording enabled. It then
// returns the caller parser's public Parse result through the D3 seam.
// The probe cannot change the caller tree, runtime, or route counters.
func DiagnosticParseWithDropCohortFrontierObserverForTest(
	parser *Parser,
	source []byte,
	observer DiagnosticParserCoreFrontierObserverForTest,
) (tree *Tree, published int, candidateErr, err error) {
	if parser == nil || parser.language == nil {
		return nil, 0, &diagnosticParserCoreDecline{
				boundary: DiagnosticParserCoreRoute,
				detail:   "frontier observer requires a parser language",
			}, &diagnosticParserCoreDecline{
				boundary: DiagnosticParserCoreRoute,
				detail:   "frontier observer requires a parser language",
			}
	}
	parser.SetAdmissionCandidateRoute(true)
	probeParser := NewParser(parser.language)
	probeParser.SetAdmissionCandidateRoute(true)
	runner, err := newAdmissionCandidateRunner(probeParser)
	if err != nil {
		return nil, 0, nil, err
	}
	runner.certificateAdmissionEnabled = true
	runner.frontierRecordingEnabled = true
	seedObserver := diagnosticParserCoreSeedObserver{
		frontierPublished: func(scheduler *diagnosticParserCoreGenericScheduler, owner core.SchedulerTransactionToken, dropIndices []int) error {
			published++
			if observer != nil {
				observer(diagnosticParserCoreFrontierObserverPayload(scheduler, owner, dropIndices))
			}
			return nil
		},
	}
	candidateTree, candidateErr := runner.parseWithObserver(source, seedObserver)
	if candidateTree != nil {
		candidateTree.Release()
	}
	runner.certificateAdmissionEnabled = false
	runner.frontierRecordingEnabled = false
	runner.options.recordDropCohortCertificates = false
	runner.options.recordDropCohortFrontiers = false
	restore := parser.DiagnosticEnableDropCohortCertificateAdmissionForTest()
	tree, err = parser.Parse(source)
	restore()
	return tree, published, candidateErr, err
}

// DiagnosticParseCandidateWithDropCohortFrontierModeForTest runs one direct
// candidate probe, then one production fallback with candidate routing held.
// The returned candidate error identifies the exact candidate boundary and
// detail. The recording flag affects only the D6a frontier producer.
func DiagnosticParseCandidateWithDropCohortFrontierModeForTest(
	parser *Parser,
	source []byte,
	record bool,
	observer DiagnosticParserCoreFrontierObserverForTest,
) (tree *Tree, published int, candidateErr, err error) {
	if parser == nil || parser.language == nil {
		return nil, 0, nil, &diagnosticParserCoreDecline{
			boundary: DiagnosticParserCoreRoute,
			detail:   "frontier mode probe requires a parser language",
		}
	}
	runner, err := parser.acquireAdmissionCandidateRunner()
	if err != nil {
		return nil, 0, nil, err
	}
	runner.certificateAdmissionEnabled = true
	runner.frontierRecordingEnabled = record
	seedObserver := diagnosticParserCoreSeedObserver{}
	if record {
		seedObserver.frontierPublished = func(scheduler *diagnosticParserCoreGenericScheduler, owner core.SchedulerTransactionToken, dropIndices []int) error {
			published++
			if observer != nil {
				observer(diagnosticParserCoreFrontierObserverPayload(scheduler, owner, dropIndices))
			}
			return nil
		}
	}
	var candidateTree *Tree
	candidateTree, candidateErr = runner.parseWithObserver(source, seedObserver)
	if candidateTree != nil {
		candidateTree.Release()
	}
	parser.admissionRouteSuppressed++
	tree, err = parser.Parse(source)
	parser.admissionRouteSuppressed--
	runner.options.recordDropCohortCertificates = false
	runner.options.recordDropCohortFrontiers = false
	runner.certificateAdmissionEnabled = false
	runner.frontierRecordingEnabled = false
	return tree, published, candidateErr, err
}

// DiagnosticEnableDropCohortFrontierVerificationForTest enables the D6a
// producer and D6b consumer for one cached candidate parse. The closure is
// idempotent and restores the runner before cached reuse.
func (p *Parser) DiagnosticEnableDropCohortFrontierVerificationForTest(observer DiagnosticParserCoreFrontierObserverForTest) func() {
	if p == nil || !p.admissionCandidateFullParseEligible(nil, true) {
		return func() {}
	}
	runner, err := p.acquireAdmissionCandidateRunner()
	if err != nil || runner == nil {
		return func() {}
	}
	token := &dropCohortActivationToken{marker: 2}
	previousObserver := runner.frontierPublishedObserver
	runner.frontierVerificationToken = token
	runner.certificateAdmissionEnabled = true
	runner.frontierRecordingEnabled = true
	runner.frontierVerificationEnabled = true
	runner.frontierPublishedObserver = func(scheduler *diagnosticParserCoreGenericScheduler, owner core.SchedulerTransactionToken, dropIndices []int) error {
		if observer != nil {
			observer(diagnosticParserCoreFrontierObserverPayload(scheduler, owner, dropIndices))
		}
		return nil
	}
	restored := false
	return func() {
		if restored {
			return
		}
		restored = true
		cached, ok := p.admissionCandidateRunner.(*parserCoreFreshFullRunner)
		if !ok || cached == nil || cached.frontierVerificationToken != token {
			return
		}
		cached.certificateAdmissionEnabled = false
		cached.frontierRecordingEnabled = false
		cached.frontierVerificationEnabled = false
		cached.frontierVerificationToken = nil
		cached.frontierPublishedObserver = previousObserver
		cached.options.recordDropCohortCertificates = false
		cached.options.recordDropCohortFrontiers = false
		cached.options.verifyDropCohortFrontiers = false
	}
}

// DiagnosticDropCohortSnapshotForTest returns the cached candidate runner's
// verifier telemetry. It is available only in the focused parser-core tests.
func (p *Parser) DiagnosticDropCohortSnapshotForTest() []byte {
	if p == nil {
		return nil
	}
	runner, ok := p.admissionCandidateRunner.(*parserCoreFreshFullRunner)
	if !ok || runner == nil || runner.compact == nil {
		return nil
	}
	return runner.compact.DiagnosticDropCohortSnapshotForTest()
}
