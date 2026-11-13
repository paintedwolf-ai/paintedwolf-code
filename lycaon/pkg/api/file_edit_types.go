package api

// FileEditSource names the tool result list a file edit reference indexes.
type FileEditSource string

const (
	// FileEditSourceFileEdit indexes ToolResult.FileEdits.
	FileEditSourceFileEdit FileEditSource = "file_edit"
	// FileEditSourceOverlayPromotion indexes ToolResult.OverlayPromotion.Files.
	FileEditSourceOverlayPromotion FileEditSource = "overlay_promotion"
)
