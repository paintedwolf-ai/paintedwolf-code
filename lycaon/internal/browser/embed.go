package browser

import (
	_ "embed"
)

// SemanticDriverJS is the app-agnostic page driver injected over CDP, built
// from lycaon-den/src/platform/semantic-driver/inject.ts with
// `bun build --minify --target=browser`.
//
//go:embed embed/semantic_driver.js
var SemanticDriverJS string
