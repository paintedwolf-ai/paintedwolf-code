package survey

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/tools"
)

func grepPathNotFound(path string) error {
	return &tools.ToolReject{
		Code: "GREP_PATH_NOT_FOUND",
		Data: map[string]any{"path": path},
	}
}

func grepRootStatErr(path string, err error) error {
	if os.IsNotExist(err) {
		return grepPathNotFound(path)
	}
	return fmt.Errorf("grep failed: %w", err)
}
