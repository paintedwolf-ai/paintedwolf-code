// Package app is the composition root for lycaon serve.
//
// Build(ctx, cfg) constructs a serveBuilder and runs an ordered list of phase
// methods, each defined in a topical build_*.go file. configuration.Config loading lives in
// load.go. ServeApp.Run starts HTTP and registered background runners.
package app
