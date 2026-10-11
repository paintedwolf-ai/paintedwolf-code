package survey

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
)

func grepPathNotFound(path string) error {
	return &toolrejection.ToolReject{
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
