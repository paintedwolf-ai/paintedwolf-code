// Package commandconformance is the differential corpus of the command language:
// shell programs that run unchanged through the command entry and in the pinned
// reference userlands, with each userland's result stored as a snapshot.
//
// A case names the userlands it compares with. The command entry passes a case
// when its status, output streams and tracked files equal one of those
// userlands' results once the deliberate differences are normalized:
//
//   - The command language always runs with pipefail and, in bash, globstar; the
//     reference programs enable them in a prelude ([Prelude]).
//   - The command entry runs in a real project directory; its absolute path stands
//     for the reference workspace, /workspace.
//   - The command entry reports a shell error as "bash: ", without the line
//     number a reference shell running -c adds.
//
// One deliberate difference is a contract case rather than a normalization: a
// producer ended by SIGPIPE because its reader finished does not fail a pipeline
// under the language's pipefail, while PIPESTATUS still reports 141.
//
// Host decisions, approvals and receipts are structured results outside the
// output streams. A differential case must finish with no refusal and no fallback
// to an installed program, so none of them reaches a comparison.
//
// Snapshots are captured from the verified images by ./task conformance:capture.
// Each snapshot records the digest of its input and the images it came from, so
// a changed case or image needs a new capture before any comparison runs.
package commandconformance
