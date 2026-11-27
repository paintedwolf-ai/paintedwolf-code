package portablepythonconfig

import _ "embed"

// BundledManifestYAML holds the build-embedded Portable Python native engine
// release manifest pinned by scripts/select-portable-python-engine.sh.
//
//go:embed bundled-manifest.yaml
var BundledManifestYAML []byte
