package main

import "time"

type runConfig struct {
	binary     string
	output     string
	scale      string
	iterations int
	prompts    int
	soak       time.Duration
	enforce    bool
	keep       bool
}

type report struct {
	Version     int                      `json:"version"`
	StartedAt   time.Time                `json:"started_at"`
	DurationMS  float64                  `json:"duration_ms"`
	Scale       string                   `json:"scale"`
	Fixture     fixtureShape             `json:"fixture"`
	Iterations  int                      `json:"iterations"`
	Prompts     int                      `json:"prompts"`
	SoakSeconds float64                  `json:"soak_seconds,omitempty"`
	Metrics     map[string]metricSummary `json:"metrics"`
	Resources   resourceSummary          `json:"resources"`
	Correctness correctnessSummary       `json:"correctness"`
	Budgets     budgets                  `json:"budgets"`
	Violations  []string                 `json:"violations"`
	ScratchDir  string                   `json:"scratch_dir,omitempty"`
}

type metricSummary struct {
	Count  int     `json:"count"`
	MinMS  float64 `json:"min_ms"`
	MeanMS float64 `json:"mean_ms"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	P99MS  float64 `json:"p99_ms"`
	MaxMS  float64 `json:"max_ms"`
}

type resourceSummary struct {
	Samples           int     `json:"samples"`
	PeakRSSBytes      int64   `json:"peak_rss_bytes,omitempty"`
	RSSGrowthBytes    int64   `json:"rss_growth_bytes,omitempty"`
	PeakFDs           int     `json:"peak_fds,omitempty"`
	FDGrowth          int     `json:"fd_growth,omitempty"`
	PeakHeapBytes     uint64  `json:"peak_heap_bytes,omitempty"`
	HeapGrowthBytes   int64   `json:"heap_growth_bytes,omitempty"`
	PeakGoroutines    int     `json:"peak_goroutines,omitempty"`
	GoroutineGrowth   int     `json:"goroutine_growth,omitempty"`
	DBReaderWaitMS    float64 `json:"db_reader_wait_ms,omitempty"`
	DBWriterWaitMS    float64 `json:"db_writer_wait_ms,omitempty"`
	PeakDBReaderOpen  int     `json:"peak_db_reader_open,omitempty"`
	PeakDBReaderInUse int     `json:"peak_db_reader_in_use,omitempty"`
	PeakDBWriterOpen  int     `json:"peak_db_writer_open,omitempty"`
	PeakDBWriterInUse int     `json:"peak_db_writer_in_use,omitempty"`
	WALBytes          int64   `json:"wal_bytes,omitempty"`
}

type correctnessSummary struct {
	HTTPFailures         int    `json:"http_failures"`
	EventCount           int    `json:"event_count"`
	DuplicateEventIDs    int    `json:"duplicate_event_ids"`
	PromptTurnsSettled   int    `json:"prompt_turns_settled"`
	StreamReplaysDone    int    `json:"stream_replays_done"`
	PerfRecordsDropped   uint64 `json:"perf_records_dropped"`
	SQLiteQuickCheck     string `json:"sqlite_quick_check"`
	Restarts             int    `json:"restarts"`
	RestartRecovered     bool   `json:"restart_recovered"`
	ReplayResetRecovered bool   `json:"replay_reset_recovered"`
	PostRestartEvents    int    `json:"post_restart_events"`
	SoakCycles           int    `json:"soak_cycles"`
}

type budgets struct {
	LatencyP95MS map[string]float64 `json:"latency_p95_ms"`
	Resources    map[string]float64 `json:"resources"`
}
