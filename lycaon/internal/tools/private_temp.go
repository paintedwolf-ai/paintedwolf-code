package tools

import "github.com/lycaon/lycaon/internal/confine"

// PrivateTempOperand locates a file in the same mount view as this invocation's processes.
func PrivateTempOperand(tc ToolContext, path string) (string, bool) {
	temp := confine.PrivateTempFor(tc.Host.SessionScratchDir)
	if _, ok := temp.Resolve(path); !ok {
		return "", false
	}
	reads := append(ConfineReadRootsForAction(tc), tc.Host.SessionOutputDir)
	return confine.ProjectPrivateTemp(tc.Identity.ProjectID, tc.Host.SessionScratchDir,
		ConfineRootsForAction(tc), tc.Files.GrantedWriteRoots, reads).Resolve(path)
}
