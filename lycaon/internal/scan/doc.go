// Package scan coordinates durable scan admission, assessments, finding history,
// and ledger queries over one SQLStore. Execution and cadence hold their own
// process-local scheduling state and depend on this persistence boundary.
//
// catalog validates scanner definitions; configuration loads host scan settings;
// output parses scanner reports; findings builds and compares finding values;
// ignores evaluates project decisions; toolapi presents scan tools and receipts.
// Driver packages execute the selected scanner against published source snapshots.
package scan
