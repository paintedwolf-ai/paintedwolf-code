package migrations

import _ "embed"

//go:embed link_kind.go
var linkKindImplementation []byte

//go:embed link_table_rebuild.sql
var linkTableRebuildSQL []byte

// SourceLinkKind lets source tables record symbolic links as their own entry
// kind and state, preserving every earlier row, index, trigger and foreign key.
// The part's source is this rebuild and its table template.
func SourceLinkKind() Part {
	return Part{Source: append(append([]byte{}, linkKindImplementation...), linkTableRebuildSQL...), RebuildTables: true, Apply: migrateLinkKind}
}
