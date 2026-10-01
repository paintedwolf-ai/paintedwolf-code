//go:build gts_parsercorephase0 && !gts_no_parsercorephase0

package gotreesitter

import (
	"errors"
	"fmt"
)

// DiagnosticParserCoreVersionLexerProductionActivationForTest runs the
// production fresh-full scheduler and returns its activation receipt. The
// external test package supplies the Swift language through the public grammar
// registry, so the test enters the same runner used by Parser.Parse.
func DiagnosticParserCoreVersionLexerProductionActivationForTest(
	lang *Language,
	source []byte,
) (DiagnosticParserCoreGenericScheduler, error) {
	if lang == nil {
		return DiagnosticParserCoreGenericScheduler{}, fmt.Errorf("swift language is unavailable")
	}
	parser := NewParser(lang)
	runner, err := newAdmissionCandidateRunner(parser)
	if err != nil {
		return DiagnosticParserCoreGenericScheduler{}, err
	}
	runner.options.ReceiptMode = DiagnosticParserCoreReceiptFull
	if err := runner.compact.Reset(); err != nil {
		return DiagnosticParserCoreGenericScheduler{}, err
	}
	tokenSource := parser.acquireParserDFATokenSource(source)
	if tokenSource == nil {
		return DiagnosticParserCoreGenericScheduler{}, fmt.Errorf("acquire parser DFA token source returned nil")
	}
	defer tokenSource.Close()
	scheduler, err := executeDiagnosticParserCoreGenericSchedulerFromSeedInto(
		&runner.scheduler, runner.compact, tokenSource, &runner.scannerScratch,
		lang.InitialState, runner.options, diagnosticParserCoreSeedObserver{},
	)
	if err != nil {
		return DiagnosticParserCoreGenericScheduler{}, err
	}
	if scheduler == nil || scheduler.receipt == nil {
		return DiagnosticParserCoreGenericScheduler{}, fmt.Errorf("production scheduler returned no receipt")
	}
	if resumeErr := scheduler.run(); !errors.Is(resumeErr, errDiagnosticParserCoreTerminalSchedulerResume) {
		return DiagnosticParserCoreGenericScheduler{}, fmt.Errorf("terminal activation resumed: %v", resumeErr)
	}
	return *scheduler.receipt, nil
}
