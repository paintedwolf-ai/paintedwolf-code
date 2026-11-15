package prompts

import "slices"

func withoutResearchTools(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == "web_search" || name == "fetch_url" })
}
