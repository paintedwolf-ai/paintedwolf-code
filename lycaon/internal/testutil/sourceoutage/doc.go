// Package sourceoutage serves a test's source directory over loopback NFS
// through a relay the test can cut and restore, so source-transport outages
// are rehearsed against the operating system's real NFS client.
//
// A test process mounts a Volume; the tests it starts as helper processes cut
// and restore it through the control socket named by EnvControl.
package sourceoutage

// EnvControl names the control socket of a mounted Volume for the helper
// processes a test starts.
const EnvControl = "PW_SOURCE_OUTAGE_CONTROL"
