package migrations

import _ "embed"

//go:embed link_kind.go
var linkKindImplementation []byte

//go:embed link_table_rebuild.sql
var linkTableRebuildSQL []byte

// SourceLinkKind lets source tables record symbolic links as their own entry
// kind and state, preserving every earlier row, index, trigger and foreign key.
// The step's source is this rebuild and its table template.
func SourceLinkKind(target Baseline) Step {
	source := append(append([]byte{}, linkKindImplementation...), linkTableRebuildSQL...)
	return Step{ID: "portable-source-link-kind", Source: source, RebuildTables: true,
		Checksum: "280d02156ff51fb374e9babb5429f5e2075a70fe9f58faaa244f83609667cb41",
		From:     Baseline{Revision: 10, Shape: "15fd7ac9114f4b71c630d00dc68fc8ba245472146e7f94660f752f0b675c59c6"},
		To:       target, Apply: migrateLinkKind}
}
