package repomap

const (
	defaultStatsMaxFiles = 50
	maxStatsMaxFiles     = 200
	maxStatsFileBytes    = 256 * 1024
)

// StatsOptions control a stats-mode repo_map call.
type StatsOptions struct {
	Root         string
	Subpath      string
	MaxFiles     int
	PathIncluded func(relSlash string, isDir bool) bool
}

// StatsFileEntry is one text file in a stats snapshot.
type StatsFileEntry struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Lines  int    `json:"lines,omitempty"`
	IsText bool   `json:"is_text"`
}

// StatsSnapshot is returned when repo_map mode=stats.
type StatsSnapshot struct {
	Mode             string           `json:"mode"`
	Path             string           `json:"path"`
	Files            int              `json:"files"`
	Dirs             int              `json:"dirs"`
	TotalBytes       int64            `json:"total_bytes"`
	TextLines        int              `json:"text_lines,omitempty"`
	LargestText      []StatsFileEntry `json:"largest_text,omitempty"`
	OrientationBrief string           `json:"orientation_brief,omitempty"`
}
