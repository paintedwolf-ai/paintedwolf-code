package migrations

// manifestLayoutSource is the shipped revision-4 shape.
var manifestLayoutSource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// SourceManifestLayout moves retained source snapshots onto per-root,
// content-defined manifests and drops the unreferenced bucket chunks.
func SourceManifestLayout(target Baseline) Step {
	return Step{
		ID: "source-manifest-layout", Source: append(append([]byte{}, manifestLayoutImplementation...), manifestLayoutSchema...),
		Checksum: "fc0cbdf1f48df9fd7730f8f35db355d2b32e2e687e80d6ec9eff09258a9b221c",
		From:     manifestLayoutSource, To: target, Apply: migrateManifestLayout, Validate: validateManifestLayout,
		RetainedCopy: retainedManifestCopy(),
	}
}
