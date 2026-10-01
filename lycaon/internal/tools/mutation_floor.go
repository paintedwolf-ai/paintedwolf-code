package tools

import "github.com/lycaon/lycaon/internal/protectedpath"

// repositoryMetadataGlobs identify repository metadata native tools read but never write.
var repositoryMetadataGlobs = []string{
	".git/**",
}

// IsSensitivePath reports credential files and repository metadata, which
// read-side tools route through read and Git operations.
func IsSensitivePath(rel string) bool {
	return protectedpath.IsCredentialFile(rel) || IsRepositoryMetadataPath(rel)
}

// IsRepositoryMetadataPath reports a path inside .git.
func IsRepositoryMetadataPath(rel string) bool {
	return protectedpath.MatchPathGlobs(repositoryMetadataGlobs, rel)
}
