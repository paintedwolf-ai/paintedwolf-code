package findings

import "strings"

// NormalizePackageEcosystem maps package-manager labels to advisory coordinates.
func NormalizePackageEcosystem(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "go", "golang", "gomod", "go-module":
		return "go"
	case "npm", "node", "nodejs", "yarn", "pnpm":
		return "npm"
	case "pypi", "pip", "pipenv", "poetry", "python":
		return "pypi"
	case "cargo", "crate", "crates.io", "rust":
		return "crates.io"
	case "bundler", "gem", "rubygems", "ruby":
		return "rubygems"
	case "maven", "gradle", "java":
		return "maven"
	case "nuget", "dotnet":
		return "nuget"
	default:
		return value
	}
}
