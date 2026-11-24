package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

const (
	MaxConcurrentProjectRuns    = 4
	DefaultMinGapMinutes        = 15
	MinStandingGapMinutes       = 15
	MaxStandingGapMinutes       = 1440
	DefaultRunSpendLimitUSD     = 2.00
	MinRunSpendLimitUSD         = 0.10
	MaxRunSpendLimitUSD         = 100.00
	DefaultDailyRunCap          = 24
	MinDailyRunCap              = 1
	MaxDailyRunCap              = 96
	MinEveryMinutes             = 15
	MaxEveryMinutes             = 10080
	MaxConsecutiveFailures      = 3
	CompletedRetentionWindow    = 30 * 24 * time.Hour
	MaxCatchupWindow            = 24 * time.Hour
	standingTimerPrefix         = "standing-timer:"
	standingGapPrefix           = "standing-gap:"
	standingRetryPrefix         = "standing-retry:"
	standingRetentionKey        = "standing-retention"
	standingLastEvalCursorKey   = "standing:last_eval"
	standingPendingChangeKey    = "standing:pending_change"
	NoticeStandingWatchPaused   = "standing_watch_paused"
	NoticeStandingWatchDisabled = "standing_watch_disabled"
)

type WatchRunStatus string

const (
	RunPending          WatchRunStatus = "pending"
	RunQueued           WatchRunStatus = "queued"
	RunRunning          WatchRunStatus = "running"
	RunWaitingForOwner  WatchRunStatus = "waiting_for_owner"
	RunCompleted        WatchRunStatus = "completed"
	RunFailed           WatchRunStatus = "failed"
	RunFenced           WatchRunStatus = "fenced"
	RunCanceled         WatchRunStatus = "canceled"
	RunSkippedOverlap   WatchRunStatus = "skipped_overlap"
	RunSkippedUnchanged WatchRunStatus = "skipped_unchanged"
	RunSkippedDailyCap  WatchRunStatus = "skipped_daily_cap"
	RunSkippedBusy      WatchRunStatus = "skipped_busy"
	RunMissed           WatchRunStatus = "missed"
)

type StandingTarget struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
}

type StandingKeptFrom struct {
	SessionID string `json:"session_id"`
	Handle    string `json:"handle"`
}

// StandingWatch is the complete person-owned watch record.
type StandingWatch struct {
	WatchID             string                 `json:"watch_id"`
	ProjectID           string                 `json:"project_id"`
	OwnerPersonID       string                 `json:"owner_person_id"`
	Name                string                 `json:"name"`
	Prompt              string                 `json:"prompt"`
	Conditions          []awaitstore.Condition `json:"conditions"`
	Gate                *awaitstore.Condition  `json:"gate"`
	MinGapMinutes       int                    `json:"min_gap_minutes"`
	Target              StandingTarget         `json:"target"`
	WorkflowID          string                 `json:"workflow_id"`
	ModelID             string                 `json:"model_id"`
	RunSpendLimitUSD    float64                `json:"run_spend_limit_usd"`
	AllowUnpriced       bool                   `json:"allow_unpriced"`
	DailyRunCap         int                    `json:"daily_run_cap"`
	TimeZone            string                 `json:"time_zone"`
	State               State                  `json:"state"`
	StateReason         string                 `json:"state_reason,omitempty"`
	KeptFrom            *StandingKeptFrom      `json:"kept_from"`
	LastOutcome         string                 `json:"last_outcome,omitempty"`
	NextRuns            []string               `json:"next_runs,omitempty"`
	ConsecutiveFailures int                    `json:"consecutive_failures"`
	Revision            int                    `json:"revision"`
	IntervalAnchor      time.Time              `json:"-"`
	Authority           Authority              `json:"-"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
	EndedAt             *time.Time             `json:"ended_at,omitempty"`
}

// StandingWatchInput supplies full record fields when creating or keeping a standing watch.
type StandingWatchInput struct {
	ProjectID        string                 `json:"project_id"`
	Name             string                 `json:"name"`
	Prompt           string                 `json:"prompt"`
	Conditions       []awaitstore.Condition `json:"conditions"`
	Gate             *awaitstore.Condition  `json:"gate,omitempty"`
	MinGapMinutes    *int                   `json:"min_gap_minutes,omitempty"`
	Target           *StandingTarget        `json:"target,omitempty"`
	WorkflowID       string                 `json:"workflow_id,omitempty"`
	ModelID          string                 `json:"model_id,omitempty"`
	RunSpendLimitUSD *float64               `json:"run_spend_limit_usd,omitempty"`
	AllowUnpriced    *bool                  `json:"allow_unpriced,omitempty"`
	DailyRunCap      *int                   `json:"daily_run_cap,omitempty"`
	TimeZone         string                 `json:"time_zone,omitempty"`
	Authority        Authority              `json:"-"`
}

// StandingWatchPatch carries partial edits to an existing standing watch.
type StandingWatchPatch struct {
	Name             *string                `json:"name,omitempty"`
	Prompt           *string                `json:"prompt,omitempty"`
	Conditions       []awaitstore.Condition `json:"conditions,omitempty"`
	SetGate          bool                   `json:"-"`
	Gate             *awaitstore.Condition  `json:"gate,omitempty"`
	MinGapMinutes    *int                   `json:"min_gap_minutes,omitempty"`
	Target           *StandingTarget        `json:"target,omitempty"`
	WorkflowID       *string                `json:"workflow_id,omitempty"`
	ModelID          *string                `json:"model_id,omitempty"`
	RunSpendLimitUSD *float64               `json:"run_spend_limit_usd,omitempty"`
	AllowUnpriced    *bool                  `json:"allow_unpriced,omitempty"`
	DailyRunCap      *int                   `json:"daily_run_cap,omitempty"`
	TimeZone         *string                `json:"time_zone,omitempty"`
	Enable           bool                   `json:"enable,omitempty"`
}

// WatchRun records one scheduled, change-triggered, or manual standing-watch run.
type WatchRun struct {
	RunID           string          `json:"run_id"`
	WatchID         string          `json:"watch_id"`
	ProjectID       string          `json:"project_id"`
	RunKey          string          `json:"run_key"`
	Status          WatchRunStatus  `json:"status"`
	ScheduledFor    time.Time       `json:"scheduled_for"`
	SessionID       string          `json:"session_id,omitempty"`
	MissedCount     int             `json:"missed_count"`
	ConditionDetail json.RawMessage `json:"condition_detail"`
	PromptSnapshot  string          `json:"-"`
	GateSnapshot    string          `json:"-"`
	SpendLimitUSD   float64         `json:"spend_limit_usd"`
	AllowUnpriced   bool            `json:"allow_unpriced"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	EndedAt         *time.Time      `json:"ended_at,omitempty"`
}

// StandingChatProvenance records which standing watch created a run chat.
type StandingChatProvenance struct {
	SessionID  string    `json:"session_id"`
	ProjectID  string    `json:"project_id"`
	WatchID    string    `json:"watch_id"`
	WatchName  string    `json:"watch_name"`
	RunID      string    `json:"run_id"`
	RecordedAt time.Time `json:"recorded_at"`
}

// StandingRunDispatchOutcome reports how the host dispatcher started or settled a run.
type StandingRunDispatchOutcome struct {
	SessionID        string
	ModelUnavailable bool
	BusyRetry        bool
}

type ModelPriceCheck func(ctx context.Context, projectID, modelID string) (priced bool, err error)
type PersonAccessCheck func(ctx context.Context, projectID, personID string) (allowed bool, err error)
type SessionTargetCheck func(ctx context.Context, projectID, sessionID string) (exists bool, busy bool, err error)
type ProjectDefaultsFunc func(ctx context.Context, projectID string) (workflowID, modelID, timeZone string, authority Authority)
type StandingRunDispatcher func(ctx context.Context, sw StandingWatch, run WatchRun, promptText string) (StandingRunDispatchOutcome, error)
type StandingNoticeEmitter func(ctx context.Context, projectID, watchID, watchName, code, reason string, clear bool)
type WatchEventPublisher func(ctx context.Context, tx *sql.Tx, topic, projectID, sessionID, watchID, runID string, revision int)

// StandingCallbacks bundles host integration hooks for standing watches.
type StandingCallbacks struct {
	ModelPriceCheck    ModelPriceCheck
	PersonAccessCheck  PersonAccessCheck
	SessionTargetCheck SessionTargetCheck
	ProjectDefaults    ProjectDefaultsFunc
	RunDispatcher      StandingRunDispatcher
	NoticeEmitter      StandingNoticeEmitter
	EventPublisher     WatchEventPublisher
	EventCommitNotify  func()
}

// StandingEngine manages person-owned standing watches and their run lifecycle.
type StandingEngine struct {
	store *Store
	mu    sync.RWMutex
	cb    StandingCallbacks
}

func newStandingEngine(store *Store) *StandingEngine {
	return &StandingEngine{store: store}
}

func (e *StandingEngine) SetCallbacks(cb StandingCallbacks) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.cb = cb
	e.mu.Unlock()
}

func (e *StandingEngine) callbacks() StandingCallbacks {
	if e == nil {
		return StandingCallbacks{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cb
}

type RevisionConflictError struct {
	WatchID         string
	CurrentRevision int
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("WATCH_REVISION_CONFLICT: watch %s is at revision %d", e.WatchID, e.CurrentRevision)
}

type SpendUnenforceableError struct{ ModelID string }

func (e *SpendUnenforceableError) Error() string {
	return fmt.Sprintf("WATCH_SPEND_UNENFORCEABLE: model %q has no pricing data", e.ModelID)
}

type RunActiveError struct{ WatchID, RunID string }

func (e *RunActiveError) Error() string {
	return fmt.Sprintf("WATCH_RUN_ACTIVE: watch %s already has active run %s", e.WatchID, e.RunID)
}

type DailyCapReachedError struct {
	WatchID     string
	DailyRunCap int
}

func (e *DailyCapReachedError) Error() string {
	return fmt.Sprintf("WATCH_DAILY_CAP_REACHED: watch %s reached daily cap %d", e.WatchID, e.DailyRunCap)
}

type NameTakenError struct{ Name string }

func (e *NameTakenError) Error() string {
	return fmt.Sprintf("WATCH_NAME_TAKEN: standing watch name %q already exists in project", e.Name)
}

type NotOwnerError struct{ WatchID string }

func (e *NotOwnerError) Error() string {
	return fmt.Sprintf("WATCH_NOT_OWNER: caller does not own standing watch %s", e.WatchID)
}

type NotPersonOwnedError struct{ WatchID string }

func (e *NotPersonOwnedError) Error() string {
	return fmt.Sprintf("WATCH_NOT_PERSON_OWNED: watch %s is not person-owned", e.WatchID)
}

type KeepUnavailableError struct{ Handle, Reason string }

func (e *KeepUnavailableError) Error() string {
	return fmt.Sprintf("WATCH_KEEP_UNAVAILABLE (%s): %s", e.Handle, e.Reason)
}

// FormatEveryMinutesSummary formats every_minutes using the largest exact unit.
func FormatEveryMinutesSummary(minutes int64) string {
	switch {
	case minutes == 10080:
		return "Every week"
	case minutes > 0 && minutes%1440 == 0:
		if d := minutes / 1440; d == 1 {
			return "Every day"
		} else {
			return fmt.Sprintf("Every %d days", d)
		}
	case minutes > 0 && minutes%60 == 0:
		if h := minutes / 60; h == 1 {
			return "Every hour"
		} else {
			return fmt.Sprintf("Every %d hours", h)
		}
	default:
		return fmt.Sprintf("Every %d minutes", minutes)
	}
}

// ConvertAgentTimerForStanding converts a delay_ms agent timer for Keep; other conditions copy unchanged.
func ConvertAgentTimerForStanding(cond awaitstore.Condition, repeat bool, anchor, now time.Time, timeZone string) awaitstore.Condition {
	if cond.Kind != "timer" || cond.DelayMS <= 0 {
		return cond
	}
	out := awaitstore.Condition{Kind: "timer"}
	if repeat {
		mins := max(MinEveryMinutes, min(MaxEveryMinutes, (cond.DelayMS+59999)/60000))
		out.EveryMinutes = mins
		return out
	}
	loc, err := loadConditionLocation(timeZone)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	due := anchor.Add(time.Duration(cond.DelayMS) * time.Millisecond)
	if minDue := now.Add(time.Minute); due.Before(minDue) {
		due = minDue
	}
	localDue := due.In(loc).Truncate(time.Minute)
	if due.In(loc).After(localDue) {
		localDue = localDue.Add(time.Minute)
	}
	out.At = localDue.Format("2006-01-02T15:04")
	if strings.TrimSpace(timeZone) != "" {
		out.TimeZone = strings.TrimSpace(timeZone)
	} else {
		out.TimeZone = "UTC"
	}
	return out
}

// StandingConditionsEqual compares two condition slices by stored draft/standing fields.
func StandingConditionsEqual(a, b []awaitstore.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		ka[i] = canonicalStandingConditionKey(a[i])
		kb[i] = canonicalStandingConditionKey(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func canonicalStandingConditionKey(c awaitstore.Condition) string {
	switch c.Kind {
	case "timer":
		days, times := "", ""
		if c.Calendar != nil {
			d, t := append([]string(nil), c.Calendar.Days...), append([]string(nil), c.Calendar.Times...)
			sort.Strings(d)
			sort.Strings(t)
			days, times = strings.Join(d, ","), strings.Join(t, ",")
		}
		return fmt.Sprintf("timer|delay=%d|every=%d|at=%s|tz=%s|days=%s|times=%s",
			c.DelayMS, c.EveryMinutes, strings.TrimSpace(c.At), strings.TrimSpace(c.TimeZone), days, times)
	case "git_ref":
		return fmt.Sprintf("git_ref|root=%s|ref=%s", strings.TrimSpace(c.Root), strings.TrimSpace(c.Ref))
	case "file_change":
		return fmt.Sprintf("file_change|root=%s|path=%s|glob=%s",
			strings.TrimSpace(c.Root), strings.TrimSpace(c.Path), strings.TrimSpace(c.Glob))
	default:
		raw, err := json.Marshal(c)
		if err != nil {
			return c.Kind
		}
		return string(raw)
	}
}

// PreviewStandingSchedule computes the next count firing instants across all timer alternatives.
func PreviewStandingSchedule(conditions []awaitstore.Condition, timeZone string, anchor, now time.Time, count int) ([]time.Time, error) {
	if count <= 0 {
		return nil, nil
	}
	var merged []time.Time
	for _, cond := range conditions {
		if cond.Kind != "timer" {
			continue
		}
		c := cond
		if strings.TrimSpace(c.TimeZone) == "" {
			c.TimeZone = timeZone
		}
		occs, err := NextTimerOccurrences(c, anchor, now, count)
		if err != nil {
			return nil, err
		}
		merged = append(merged, occs...)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Before(merged[j]) })
	out := make([]time.Time, 0, count)
	for _, t := range merged {
		if len(out) == 0 || !out[len(out)-1].Equal(t) {
			out = append(out, t)
			if len(out) == count {
				break
			}
		}
	}
	return out, nil
}
